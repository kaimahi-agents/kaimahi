package proxy_test

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

type fixedResolver struct{ addr netip.Addr }

func (f fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{f.addr}, nil
}

// A hosted LLM upstream whose body stalls past the lifetime (or
// exceeds the cap) fails CLOSED at the proxy — a 502, never a 200 with a
// truncated payload — and the call is still ledgered, with zero tokens
// and the 502, before the failure is honored.
func TestHostedUpstreamCutBodyIsA502AndLedgered(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices": [{"message": {"content": "hel`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	hardened, err := egress.NewClient(egress.Policy{
		Resolver:     fixedResolver{netip.MustParseAddr("203.0.113.10")},
		BodyLifetime: 300 * time.Millisecond,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}, []egress.Host{{Name: "example.com", CAFile: caFile}})
	require.NoError(t, err)

	f := newFakeStore()
	f.addToken("kmh_opaque", store.Credential{Name: "hello"})
	deps := testDeps(f, map[string]config.Upstream{
		"copilot": {Protocol: config.ProtocolChatCompletions, BaseURL: "https://example.com", Path: "chat/completions",
			Classification: config.ClassMetered, Internet: true, CAFile: caFile},
	})
	deps.InternetClient = hardened
	mux := proxy.NewDataMux(deps)
	started := time.Now()
	w := doChat(t, mux, "kmh_opaque", "/upstream/copilot/chat/completions", `{"model": "gpt-5-mini", "messages": []}`)
	require.Less(t, time.Since(started), 5*time.Second, "the cut must not hold the worker")
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "upstream response cut")
	require.Len(t, f.ledger, 1)
	require.Equal(t, http.StatusBadGateway, f.ledger[0].Status)
	require.Zero(t, f.ledger[0].InputTokens+f.ledger[0].OutputTokens)
}
