package notify

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The gateway is a TLS listener even over loopback, so the plane's own
// notifier now completes a handshake before it can post. A handshake that
// fails is the one thing that must not be called ambiguous.
//
// `ambiguous` means "do not retry, somebody may already have been told". A
// verification failure happens BEFORE any request byte is written, so nothing
// was told, retrying cannot double-post, and the honest end state is the
// refused one — bounded retries, then "the request stays filed". Classified
// the other way, an expired seam certificate would stop every approval
// notification after a single attempt and log that the outcome was unknown.
func TestAFailedHandshakeIsARefusalAndNotAnUnknownOutcome(t *testing.T) {
	// A real server the client does not trust: the failure is produced by a
	// handshake rather than described by a hand-made error, so this cannot
	// pass against an error shape Go no longer returns.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unreachable")
	}))
	defer srv.Close()

	_, err := (&http.Client{Timeout: 5 * time.Second}).Get(srv.URL)
	require.Error(t, err, "a client with no custom trust must not reach a self-signed listener")
	require.Contains(t, strings.ToLower(err.Error()), "certificate")

	require.Equal(t, refused, classifyErr(err),
		"a failed handshake was classified %q; nothing was posted, so it must be retriable", classifyErr(err))
}

// The other handshake failures reach the caller as different types, and every
// one of them is still "the connection was never established".
func TestEveryHandshakeFailureShapeIsARefusal(t *testing.T) {
	// A TLS client against a PLAINTEXT server: the server's HTTP reply is
	// not a TLS record, which is the RecordHeaderError path.
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	tlsURL := "https://" + strings.TrimPrefix(plain.URL, "http://")
	_, err := (&http.Client{Timeout: 5 * time.Second}).Get(tlsURL)
	require.Error(t, err)
	require.Equal(t, refused, classifyErr(err),
		"speaking TLS to a plaintext listener was classified %q", classifyErr(err))

	// A version floor the client cannot meet: the server sends an alert.
	strict := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	strict.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	strict.StartTLS()
	defer strict.Close()
	client := strict.Client()
	transport := client.Transport.(*http.Transport)
	cfg := transport.TLSClientConfig.Clone()
	cfg.MaxVersion = tls.VersionTLS12
	transport.TLSClientConfig = cfg
	if _, err := client.Get(strict.URL); err != nil {
		require.Equal(t, refused, classifyErr(err),
			"a version mismatch was classified %q", classifyErr(err))
	}
}

// The distinction the classifier exists for has to survive: something that
// genuinely may have been delivered is still not retried.
func TestSomethingThatMayHaveBeenDeliveredIsStillNotRetried(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	_, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get(slow.URL)
	require.Error(t, err)
	require.Equal(t, ambiguous, classifyErr(err),
		"a timeout after the request went out must stay ambiguous, or the retry is a double-post")
}

// End to end through the retry loop, because the classification only matters
// for what it makes `send` do: a handshake failure has to be ATTEMPTED more
// than once and end in the message that says the request is still filed.
func TestAnUntrustedGatewayIsRetriedAndReportedAsStillFiled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	var attempts atomic.Int64
	counting := &http.Client{
		Timeout:   2 * time.Second,
		Transport: roundTripCounter{n: &attempts},
	}
	dir := t.TempDir()
	credential := dir + "/cred"
	channel := dir + "/channel"
	require.NoError(t, writeFile(credential, "kmh_"+strings.Repeat("a", 64)))
	require.NoError(t, writeFile(channel, "C0000000000"))

	p := New(Deps{
		GatewayURL:     srv.URL,
		Upstream:       "slack",
		Tool:           "post",
		CredentialFile: credential,
		ChannelFile:    channel,
		Client:         counting,
		Backoff:        []time.Duration{time.Millisecond, time.Millisecond},
		Sleep:          func(ctx context.Context, _ time.Duration) {},
	})
	// One attempt is more than one request — the MCP handshake precedes the
	// call — so the assertion is on the OUTCOME the retry loop acts on,
	// which is the thing that decides whether it retries at all.
	o, err := p.attempt(context.Background(), Post{Kind: "approval", Text: "a human decision is waiting"})
	require.Error(t, err)
	require.Equal(t, refused, o,
		"an untrusted gateway must be a refusal: nothing was posted, so the bounded retries are safe")

	before := attempts.Load()
	p.send(context.Background(), Post{Kind: "approval", Text: "a human decision is waiting"})
	require.Greater(t, attempts.Load()-before, int64(1),
		"send stopped after a single try; a handshake failure is retriable and the operator would "+
			"be told the outcome was unknown instead")
}

type roundTripCounter struct{ n *atomic.Int64 }

func (c roundTripCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	// No custom trust: the self-signed listener is refused during the
	// handshake, which is the condition under test.
	return http.DefaultTransport.RoundTrip(r)
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
