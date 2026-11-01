package copilot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"github.com/microsoft/agent-host-protocol/clients/go/ahpws"
)

var ErrHostConnection = errors.New("copilot_host_connection: local host connection refused")
var ErrHostToken = errors.New("copilot_host_token: invalid COPILOTD_CLIENT_TOKEN")

// HostAccess owns a non-refreshing native-host snapshot. On setup failure a
// nonnil result is checker-only. Observers run before fan-out and must not RPC.
type HostAccess interface {
	Client() *ahp.Client
	Info() (hostVersion, protocolVersion string)
	CheckContent(...string) error
	Validate(context.Context) error
	Close(context.Context) error
}

type hostAccess struct {
	*preview
	client                       *ahp.Client
	transport                    ahp.Transport
	keys                         []string
	hostVersion, protocolVersion string
}

func (s *hostAccess) Info() (string, string) { return s.hostVersion, s.protocolVersion }

func (s *hostAccess) String() string   { return "copilot host access" }
func (s *hostAccess) GoString() string { return s.String() }
func (s *hostAccess) reject(err error) error {
	s.stop(err)
	if s.transport != nil {
		_ = s.transport.Close(context.Background())
	}
	return err
}
func (s *hostAccess) CheckContent(fields ...string) error {
	for _, v := range fields {
		for _, key := range s.keys {
			if strings.Contains(v, key) {
				return s.reject(ErrCredentialContent)
			}
		}
	}
	return nil
}
func (s *hostAccess) Validate(ctx context.Context) error {
	if err := s.preview.Validate(ctx); err != nil {
		return s.reject(err)
	}
	return nil
}
func (s *hostAccess) Client() *ahp.Client {
	if s.Validate(s.ctx) != nil {
		return nil
	}
	return s.client
}
func (s *hostAccess) Close(ctx context.Context) error {
	s.stop(ErrHostConnection)
	if s.client != nil {
		if err := s.client.Shutdown(ctx); err != nil {
			return safeError(err)
		}
	}
	return nil
}

// CopilotdClient resolves existing GitHub sign-in only, never the HTTP catalog,
// device flow, exchange, or persistence. The endpoint is checked before auth.
func (p *Provider) CopilotdClient(ctx context.Context, endpoint string, observe func(ahptypes.ActionEnvelope) error) (HostAccess, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ws" && u.Scheme != "wss" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(endpoint, "#") {
		return nil, ErrHostConnection
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return nil, ErrHostConnection
	}
	token, set := os.LookupEnv("COPILOTD_CLIENT_TOKEN")
	p.mu.Lock()
	s := &hostAccess{preview: &preview{owner: p, generation: p.generation, ctx: ctx, until: p.now().Add(previewLifetime), now: p.now}}
	source, authErr := p.credential(ctx)
	p.mu.Unlock()
	for _, key := range []string{source, token} {
		if key == "" {
			continue
		}
		b, _ := json.Marshal(key)
		var escaped strings.Builder
		for _, r := range key {
			fmt.Fprintf(&escaped, `\u%04x`, r)
		}
		s.keys = append(s.keys, key, url.QueryEscape(key), url.PathEscape(key), string(b[1:len(b)-1]), escaped.String(), base64.StdEncoding.EncodeToString([]byte(key)))
	}
	if set && !validCredential(token) {
		return s, s.reject(ErrHostToken)
	}
	if authErr != nil {
		return s, s.reject(authErr)
	}
	if !validCredential(source) {
		return s, s.reject(ErrAuthRevoked)
	}
	if err = s.CheckContent(endpoint, u.Path); err != nil {
		return s, err
	}
	if err = s.Validate(ctx); err != nil {
		return s, err
	}
	if set {
		u.RawQuery = url.Values{"tkn": {token}}.Encode()
	}
	// A dedicated dialer has neither environment proxies nor redirects.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	transport, err := ahpws.Connect(ctx, u.String(), ahpws.DialOptions{HTTPClient: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ReadLimit: 32 << 20})
	if err != nil {
		return s, s.reject(ErrHostConnection)
	} // ahpws errors contain the URL.
	s.transport = transport
	s.client, err = ahp.Connect(ctx, &privateTransport{Transport: transport, access: s, observe: observe}, ahp.DefaultConfig())
	if err != nil {
		return s, s.reject(ErrHostConnection)
	}
	context.AfterFunc(ctx, func() { s.reject(safeError(ctx.Err())) })
	var identity [16]byte
	rand.Read(identity[:])
	result, err := s.client.Initialize(ctx, hex.EncodeToString(identity[:]), ahptypes.SupportedProtocolVersions(), []string{ahptypes.RootResourceURI})
	if err == nil {
		s.protocolVersion = result.ProtocolVersion
		if result.ServerInfo != nil && result.ServerInfo.Version != nil {
			s.hostVersion = *result.ServerInfo.Version
		}
		advertised := false
		for _, snapshot := range result.Snapshots {
			if snapshot.Resource == ahptypes.RootResourceURI && snapshot.State.Root != nil {
				for _, agent := range snapshot.State.Root.Agents {
					for _, resource := range agent.ProtectedResources {
						if resource.Resource == "https://api.github.com" {
							advertised = true
						}
					}
				}
			}
		}
		if !advertised {
			err = ErrHostConnection
		} else {
			err = s.client.Request(ctx, "authenticate", ahptypes.AuthenticateParams{Channel: ahptypes.RootResourceURI, Resource: "https://api.github.com", Token: source}, &ahptypes.AuthenticateResult{})
		}
	}
	if err != nil {
		if checked := s.Validate(ctx); checked != nil {
			return s, checked
		}
		var rpc *ahp.RPCError
		if errors.As(err, &rpc) && rpc.Code == -32007 {
			return s, s.reject(ErrAuthRevoked)
		}
		return s, s.reject(ErrHostConnection)
	}
	return s, s.Validate(ctx)
}

type privateTransport struct {
	ahp.Transport
	access  *hostAccess
	observe func(ahptypes.ActionEnvelope) error
}

func (t *privateTransport) String() string   { return "copilot private transport" }
func (t *privateTransport) GoString() string { return t.String() }
func (t *privateTransport) Send(ctx context.Context, m ahp.TransportMessage) error {
	if err := t.access.Validate(ctx); err != nil {
		return err
	}
	if t.Transport.Send(ctx, m) != nil {
		return t.access.reject(ErrHostConnection)
	}
	return nil
}
func (t *privateTransport) Recv(ctx context.Context) (ahp.TransportMessage, error) {
	s := t.access
	m, err := t.Transport.Recv(ctx)
	if err != nil {
		return ahp.TransportMessage{}, s.reject(ErrHostConnection)
	}
	b, _, err := m.Bytes()
	if err == nil {
		err = s.checkHostJSON(b)
	}
	if err != nil {
		return ahp.TransportMessage{}, s.reject(err)
	}
	parsed, err := m.IntoParsed()
	if err != nil {
		return ahp.TransportMessage{}, s.reject(ErrHostConnection)
	}
	if n := parsed.Notification; n != nil && n.Method == "action" {
		var a ahptypes.ActionEnvelope
		if json.Unmarshal(n.Params, &a) != nil || a.Channel == "" || a.ServerSeq < 1 || a.Action.Value == nil {
			return ahp.TransportMessage{}, s.reject(ErrHostConnection)
		}
		if t.observe != nil && t.observe(a) != nil {
			return ahp.TransportMessage{}, s.reject(ErrHostConnection)
		}
	}
	if err = s.Validate(s.ctx); err != nil {
		return ahp.TransportMessage{}, err
	}
	return m, nil
}

// Validate duplicate keys before typed decoding; inspect even unused strings.
func (s *hostAccess) checkHostJSON(b []byte) error {
	if len(b) > 32<<20 || !runtime.JSONKeysUnique(b) {
		return ErrHostConnection
	}
	var fields map[string]json.RawMessage
	var version string
	if json.Unmarshal(b, &fields) != nil || json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" {
		return ErrHostConnection
	}
	_, method := fields["method"]
	_, id := fields["id"]
	_, result := fields["result"]
	_, failure := fields["error"]
	if method && (result || failure) || !method && (!id || result == failure) || id && string(fields["id"]) == "null" {
		return ErrHostConnection
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	for {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrHostConnection
		}
		if v, ok := token.(string); ok {
			if err = s.CheckContent(v); err != nil {
				return err
			}
		}
	}
}
