// Package admin talks to the governance plane's admin API.
//
// It owns the admin transport, read renderings, and mutation contracts:
//
//   - The admin port (9091) is on NO Service. Reaching it takes a
//     `kubectl port-forward` to the pod — i.e. CLUSTER credentials gate every
//     operation before the admin bearer token does. That ordering is the
//     point, not an implementation detail.
//   - The forward binds 127.0.0.1 explicitly, and kmx waits for kubectl's own
//     "Forwarding from" line before it sends anything. Both halves are
//     load-bearing: the pin makes kubectl FAIL when the port is taken rather
//     than bind only the v6 side, and the line is the only proof that the
//     socket we are about to talk to is OURS. Probing /healthz first would
//     have accepted a 200 from a stale forward to a DIFFERENT cluster — and
//     then issued a credential in that plane while the Secret and the preset
//     switch landed in this one. (`kmx agent chat` learned this the same way,
//     against a real accident.)
//   - Fail closed: every call checks for a well-formed positive, and no
//     redirect is ever followed on an authenticated request.
//   - Custody: the admin bearer token exists only in this process's memory.
//     The former shell client had to spill it into a 0600 file for curl to read;
//     Go does not, so it never reaches a file, argv, the environment or a
//     log. TestTokensNeverLeaveTheProcess holds that line.
package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/portforward"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

// Namespace is where the plane lives.
const Namespace = "kaimahi"

// DefaultPort is the local side of the admin forward. A fixed default makes a
// stale forward visible as a bind failure rather than silently talking to it.
const DefaultPort = "19091"

// The admin API readiness wait: 150 × 0.2s, after the owned forward binds.
// Variables rather than constants so tests can exercise timeout paths without
// spending half a minute on each.
var (
	pollAttempts = 150
	pollInterval = 200 * time.Millisecond
)

// Kube is the sliver of kmx's kubectl plumbing this package needs. It is an
// interface so the tests can watch every argument that would be passed to a
// real kubectl without running one.
type Kube interface {
	// Capture runs kubectl with an explicit --context and returns stdout.
	Capture(args ...string) (string, error)
	// Command prepares a kubectl the caller manages itself (the forward).
	Command(args ...string) *exec.Cmd
}

// Client is an open admin session: a live port-forward plus the bearer.
type Client struct {
	base  string
	token string
	http  *http.Client
	fwd   *portforward.Forward
	// plane is what the version handshake learned, once, at Open
	// (contract.go). Every skew decision reads it rather than inferring age
	// from whatever a given response happened to omit.
	plane PlaneVersion
	// kmxVersion is this binary's own version, named in skew messages so an
	// operator can see both ends of the gap in one place.
	kmxVersion string
}

// Open reads the admin token, starts the port-forward, and waits for the
// admin API to answer.
func Open(k Kube, port string, log io.Writer) (*Client, error) {
	return OpenAs(k, port, log, version.Resolve(debug.ReadBuildInfo()).Version)
}

// OpenAs is Open with this kmx's version supplied rather than read from the
// running binary, so the skew messages are testable without a release build.
func OpenAs(k Kube, port string, log io.Writer, kmxVersion string) (*Client, error) {
	if port == "" {
		port = DefaultPort
	}
	encoded, err := k.Capture("-n", Namespace, "get", "secret", "kaimahi-admin",
		"-o", "jsonpath={.data.token}")
	if err != nil {
		return nil, fmt.Errorf("cannot read the kaimahi-admin Secret (is the plane deployed? `kmx plane`): %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("the kaimahi-admin Secret is missing or empty — run `kmx plane`")
	}

	c := &Client{
		base:       "http://127.0.0.1:" + port,
		token:      string(bytes.TrimSpace(raw)),
		kmxVersion: kmxVersion,
		// No redirects on an authenticated call, ever: Go strips
		// Authorization across hosts but not custom headers, and an admin
		// bearer must not travel anywhere it was not addressed to.
		http: &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}

	fwd, err := portforward.Start(k, Namespace, "deploy/kaimahi-proxy", port, "9091")
	if err != nil {
		return nil, fmt.Errorf("%w\n  (the admin port is on no Service; ADMIN_PORT=<free port> moves the local side)", err)
	}
	c.fwd = fwd

	// The forward is ours. Only then: is the plane answering behind it?
	answered := run.Poll(pollAttempts, pollInterval, func() bool { return c.healthy() })
	if !answered {
		c.Close()
		return nil, fmt.Errorf("the plane's admin API did not answer on the forward to 127.0.0.1:%s:\n  %s\n"+
			"  The forward is up, so this is the proxy, not the port. Check `kubectl -n %s get pods`.",
			port, fwd.Detail(), Namespace)
	}
	// The plane is answering. Ask what it is before asking it for anything,
	// so a skew is a message about versions rather than a 404 quoted back at
	// the operator (contract.go).
	if err := c.handshake(); err != nil {
		c.Close()
		return nil, err
	}
	if log != nil {
		fmt.Fprintf(log, "kubectl -n %s port-forward deploy/kaimahi-proxy %s:9091 # (the admin port is on no Service)\n",
			Namespace, port)
		// Name the plane the way the context guard names the cluster: on
		// stderr, on every run, before anything is asked of it.
		fmt.Fprintf(log, "%s\n", c.plane.Describe())
		if note := c.SkewNote(); note != "" {
			fmt.Fprintf(log, "%s\n", note)
		}
	}
	return c, nil
}

func (c *Client) healthy() bool {
	resp, err := c.http.Get(c.base + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Close tears the session's forward down.
func (c *Client) Close() { c.fwd.Close() }

// Do makes one authenticated admin call and returns the status and body.
func (c *Client) Do(method, path string, body any) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, payload)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("admin %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, out, nil
}

// Get performs a read and refuses anything but 200, quoting the body.
func (c *Client) Get(what, path string) (map[string]any, error) {
	status, body, err := c.Do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s read failed (HTTP %d): %s", what, status, strings.TrimSpace(string(body)))
	}
	return decode(body)
}

func decode(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// UseNumber so a token count is printed as it was sent, never as
	// 1.234568e+06.
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("cannot read the plane's reply: %w", err)
	}
	return out, nil
}

// ExpiresFrom reads the deadline the plane stamped on a freshly issued
// credential. Empty when the reply carries none — an older plane, and
// not a reason to fail an issue that otherwise succeeded.
func ExpiresFrom(body []byte) string {
	doc, err := decode(body)
	if err != nil {
		return ""
	}
	expires, _ := doc["expires_at"].(string)
	return expires
}

// TokenFrom reads the issued token out of a credential-creation reply.
//
// A missing or empty token is an error rather than an empty Secret: the
// token is shown exactly once, so an empty one written now is a credential
// that can never be used and never be recovered.
func TokenFrom(body []byte) (string, error) {
	doc, err := decode(body)
	if err != nil {
		return "", err
	}
	token, _ := doc["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("the plane issued the credential but returned no token")
	}
	return token, nil
}
