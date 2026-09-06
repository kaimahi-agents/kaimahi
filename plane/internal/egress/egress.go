// Package egress is the ONE hardened dialer for everything the plane
// reaches on the internet: the LLM proxy's hosted upstreams
// (Copilot) and the MCP gateway's hosted tool servers (GitHub) share the
// client built here, so neither seam's hardening is implicit.
//
// The rules, all fail-closed:
//
//   - https only, port 443 only, and only to the hosts the upstream table
//     names — the client refuses any other URL before touching the network;
//   - the host is resolved and EVERY answer is checked against the
//     private, link-local, loopback, carrier-NAT, multicast, reserved and
//     cloud-metadata ranges (169.254.169.254 first of all); one refused
//     answer refuses the call;
//   - the connection goes to the ADDRESS that was checked, never back
//     through the hostname, and every call resolves afresh — a record
//     that changes after the check (DNS rebinding) cannot get through;
//   - redirects are surfaced, never followed (standing guidance);
//   - bounded connect and response-header timeouts, a response-size cap
//     that fails the read rather than truncating silently, and a bounded
//     body lifetime — a stalled upstream body is cut at a deadline and
//     the caller sees an error, so no worker is held open;
//   - a per-upstream ca_file (mounted) replaces the system roots for that
//     one host — how CI's synthetic upstream presents a test certificate
//     without loosening anything for a real one.
//
// In-cluster upstreams never come here: they keep the plain in-cluster
// dial (config.Upstream.Internet / config.ToolUpstream.Internet select).
package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Defaults: connect and TLS handshake within 10 s; the upstream must start
// answering within 60 s (a tool call or an LLM's first token); the whole
// body — streamed or not — within 5 minutes (the bound both seams already
// ran under); no response larger than 8 MiB (the gateway's buffer).
const (
	DefaultConnectTimeout        = 10 * time.Second
	DefaultResponseHeaderTimeout = 60 * time.Second
	DefaultBodyLifetime          = 5 * time.Minute
	DefaultMaxResponseBytes      = 8 << 20
)

var (
	ErrPrivateAddress   = errors.New("egress: refused address")
	ErrScheme           = errors.New("egress: only https is dialed")
	ErrPort             = errors.New("egress: only port 443 is dialed")
	ErrUnknownHost      = errors.New("egress: host is not a configured hosted upstream")
	ErrResponseTooLarge = errors.New("egress: upstream response exceeds the size cap")
	ErrBodyLifetime     = errors.New("egress: upstream body cut at the lifetime deadline")
	// ErrNoClient is what a seam answers when an upstream is marked
	// internet but no hardened client was injected: fail closed, never
	// the plain dial.
	ErrNoClient = errors.New("egress: no hardened client configured for a hosted upstream")
)

// IsRefusal reports whether err is the egress policy refusing a call
// before any byte left — a private or metadata answer, a forbidden port
// or scheme, an unknown host, or no hardened client at all — as distinct
// from an upstream that was dialed and did not answer.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrPrivateAddress) || errors.Is(err, ErrPort) ||
		errors.Is(err, ErrScheme) || errors.Is(err, ErrUnknownHost) || errors.Is(err, ErrNoClient)
}

// IsBodyCut reports whether err is the hardened client cutting a
// response body (the size cap or the lifetime deadline).
func IsBodyCut(err error) bool {
	return errors.Is(err, ErrResponseTooLarge) || errors.Is(err, ErrBodyLifetime)
}

// Resolver is the one lookup the dialer performs. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy holds the dialer's bounds. Zero values take the defaults above.
type Policy struct {
	Resolver              Resolver
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	BodyLifetime          time.Duration
	MaxResponseBytes      int64
	// Dial connects to an already-checked "ip:443" — it receives the vetted address, never a name. Tests substitute it;
	// production always uses a net.Dialer bounded by ConnectTimeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (p Policy) withDefaults() Policy {
	if p.Resolver == nil {
		p.Resolver = net.DefaultResolver
	}
	if p.ConnectTimeout <= 0 {
		p.ConnectTimeout = DefaultConnectTimeout
	}
	if p.ResponseHeaderTimeout <= 0 {
		p.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if p.BodyLifetime <= 0 {
		p.BodyLifetime = DefaultBodyLifetime
	}
	if p.MaxResponseBytes <= 0 {
		p.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if p.Dial == nil {
		p.Dial = (&net.Dialer{Timeout: p.ConnectTimeout}).DialContext
	}
	return p
}

// refusedRange is one entry of the refusal table. The order is the order
// the reasons are reported in; the metadata endpoint comes first so its
// refusal is named for what it is rather than as generic link-local.
type refusedRange struct {
	prefix netip.Prefix
	reason string
}

var refusedRanges = []refusedRange{
	{netip.MustParsePrefix("169.254.169.254/32"), "cloud metadata endpoint"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private (RFC 1918)"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private (RFC 1918)"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private (RFC 1918)"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT (RFC 6598)"},
	{netip.MustParsePrefix("0.0.0.0/8"), "this network"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved / broadcast"},
	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("fc00::/7"), "unique-local (RFC 4193)"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
	// Tunnel prefixes carry an embedded IPv4 destination a relay would
	// unwrap; refused outright rather than decoded.
	{netip.MustParsePrefix("2002::/16"), "6to4 tunnel (embedded IPv4)"},
	{netip.MustParsePrefix("2001::/32"), "Teredo tunnel (embedded IPv4)"},
}

// nat64 and ipv4Compatible embed an IPv4 address in the low 32 bits
// (RFC 6052; the deprecated ::a.b.c.d form); the embedded address is
// what would actually be reached.
var (
	nat64          = netip.MustParsePrefix("64:ff9b::/96")
	ipv4Compatible = netip.MustParsePrefix("::/96")
)

// Refused reports why an address must not be dialed, or "" when it may.
// The documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24,
// 2001:db8::/32) are deliberately NOT here: they are neither private nor
// routable, which is what lets CI's synthetic upstream sit on one without
// loosening the list for it.
func Refused(a netip.Addr) string {
	a = a.Unmap() // ::ffff:a.b.c.d is a.b.c.d
	if a.Is6() && (nat64.Contains(a) || (ipv4Compatible.Contains(a) && a != netip.IPv6Unspecified() && a != netip.IPv6Loopback())) {
		b := a.As16()
		return Refused(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	for _, r := range refusedRanges {
		if r.prefix.Contains(a) {
			return r.reason
		}
	}
	return ""
}

// Vet resolves host (an IP literal is checked without a lookup) and
// refuses it unless EVERY address is acceptable. A lookup failure is
// returned as its own error, distinct from ErrPrivateAddress, so a caller
// can tell "the record points somewhere it must not" from "the record
// could not be read".
func Vet(ctx context.Context, r Resolver, host string) ([]netip.Addr, error) {
	if r == nil {
		r = net.DefaultResolver
	}
	var answers []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		answers = []netip.Addr{ip}
	} else {
		var err error
		answers, err = r.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("egress: resolving %s: %w", host, err)
		}
		if len(answers) == 0 {
			return nil, fmt.Errorf("egress: resolving %s: no addresses", host)
		}
	}
	for _, a := range answers {
		if why := Refused(a); why != "" {
			return nil, fmt.Errorf("%w: %s resolves to %s (%s)", ErrPrivateAddress, host, a, why)
		}
	}
	return answers, nil
}

// DialContext is the hardened dial: port 443 only, every resolved address
// vetted, then a connection to the vetted ADDRESS (an IP literal, so no
// second lookup can substitute another). Each call resolves afresh.
func (p Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	p = p.withDefaults()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	if port != "443" {
		return nil, fmt.Errorf("%w: %s", ErrPort, addr)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, p.ConnectTimeout)
	defer cancel()
	answers, err := Vet(lookupCtx, p.Resolver, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, a := range answers {
		conn, err := p.Dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, fmt.Errorf("egress: connecting to %s: %w", host, last)
}

// Host is one hosted upstream the client may reach. CAFile, when set, is
// the only trust anchor for that host (system roots otherwise).
type Host struct {
	Name   string
	CAFile string
}

// NewClient builds the one hardened client over the given hosts. Every
// ca_file is read here, at construction, so an unreadable one refuses the
// whole client loudly at boot rather than failing a call later.
func NewClient(p Policy, hosts []Host) (*http.Client, error) {
	p = p.withDefaults()
	t := &transport{p: p, byHost: map[string]*http.Transport{}}
	for _, h := range hosts {
		name := strings.ToLower(strings.TrimSuffix(h.Name, "."))
		if name == "" {
			return nil, errors.New("egress: hosted upstream with an empty host")
		}
		if _, dup := t.byHost[name]; dup {
			return nil, fmt.Errorf("egress: host %q configured twice with possibly different trust", name)
		}
		tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
		if h.CAFile != "" {
			pemBytes, err := os.ReadFile(h.CAFile)
			if err != nil {
				return nil, fmt.Errorf("egress: host %q: reading ca_file: %w", name, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemBytes) {
				return nil, fmt.Errorf("egress: host %q: ca_file %s holds no PEM certificate", name, h.CAFile)
			}
			tlsConf.RootCAs = pool
		}
		t.byHost[name] = &http.Transport{
			DialContext:           p.DialContext,
			TLSClientConfig:       tlsConf,
			TLSHandshakeTimeout:   p.ConnectTimeout,
			ResponseHeaderTimeout: p.ResponseHeaderTimeout,
			ForceAttemptHTTP2:     true,
			// No connection reuse: a pooled connection would carry the
			// vetting of an earlier call, and "every call resolves and
			// checks afresh" must hold literally — it is what CI's
			// rebinding step observes. The cost is one TLS handshake per
			// call to a hosted upstream, a few per agent turn.
			DisableKeepAlives: true,
			// Never a proxy from the environment: the address that was
			// checked is the address that is dialed.
			Proxy: nil,
		}
	}
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// transport routes each request to its host's transport after refusing
// everything the policy forbids, and bounds the response body.
type transport struct {
	p      Policy
	byHost map[string]*http.Transport
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("%w: %s", ErrScheme, req.URL.Redacted())
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("%w: %s", ErrPort, req.URL.Redacted())
	}
	name := strings.ToLower(strings.TrimSuffix(req.URL.Hostname(), "."))
	inner, ok := t.byHost[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHost, name)
	}
	// The body lifetime is a deadline on the request's context: the
	// transport aborts a body read the moment it passes, whether the
	// upstream is streaming slowly or has gone silent.
	ctx, cancel := context.WithTimeoutCause(req.Context(), t.p.BodyLifetime, ErrBodyLifetime)
	resp, err := inner.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &boundedBody{
		body:   resp.Body,
		ctx:    ctx,
		cancel: cancel,
		left:   t.p.MaxResponseBytes,
	}
	return resp, nil
}

// boundedBody enforces the size cap and names the lifetime cut. Both are
// errors the reader sees — never a silent short body.
type boundedBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelFunc
	left   int64
	done   bool // a clean EOF was already returned; keep answering it
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.EOF
	}
	if b.left <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	// Only OUR deadline is a lifetime cut; a caller that cancelled (the
	// agent went away) keeps its own error.
	//
	// io.EOF counts. Once the deadline has cut the body, whatever the stream
	// does next is not a clean end: the upstream had more to send and we
	// stopped listening. Which of the two the transport reports at the moment
	// of the cut is a race — an explicit cancellation error usually, a bare
	// EOF occasionally — and treating the second as success handed the caller
	// a SHORT body with no error at all, the silent truncation this type
	// exists to prevent. Seen once as a CI flake, where the end-to-end test's
	// ReadAll returned nil; TestALifetimeCutIsNeverACleanEndOfBody pins both
	// shapes deterministically.
	if err != nil && context.Cause(b.ctx) == ErrBodyLifetime {
		if err == io.EOF {
			return n, ErrBodyLifetime
		}
		return n, fmt.Errorf("%w: %v", ErrBodyLifetime, err)
	}
	if err == nil && b.left <= 0 {
		// Exactly at the cap: one more byte decides. Peek so a body
		// that is precisely cap-sized still ends cleanly.
		var one [1]byte
		m, perr := b.body.Read(one[:])
		if m > 0 {
			return n, ErrResponseTooLarge
		}
		// Same rule at the cap boundary: a deadline that has already fired
		// makes the peek's end-of-stream a cut, not a clean finish.
		if perr != nil && context.Cause(b.ctx) == ErrBodyLifetime {
			if perr == io.EOF {
				return n, ErrBodyLifetime
			}
			return n, fmt.Errorf("%w: %v", ErrBodyLifetime, perr)
		}
		if perr != nil && perr != io.EOF {
			return n, perr
		}
		// The peek can also come back (0, nil). Rare, and legal for an
		// io.Reader — so make the invariant total rather than nearly total:
		// no path ends this body cleanly once our deadline has fired.
		if context.Cause(b.ctx) == ErrBodyLifetime {
			return n, ErrBodyLifetime
		}
		b.done = true
		return n, io.EOF
	}
	if err == io.EOF {
		b.done = true
	}
	return n, err
}

func (b *boundedBody) Close() error {
	b.cancel()
	return b.body.Close()
}
