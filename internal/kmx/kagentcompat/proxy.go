package kagentcompat

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// legacyA2APrefixes are the ONLY request paths this hop reads, bounds or
// rewrites. The pinned CLI posts its invoke to `/api/a2a/<ns>/<agent>/`, and
// carries one sibling prefix for sandboxed agents; both are recorded in the
// v0.10.1 binary itself.
//
// Everything else the controller may ever be POSTed — today or after an
// upgrade — is forwarded untouched and unbuffered. A compatibility shim for
// one known defect has no business parsing unrelated traffic as JSON-RPC,
// and a future endpoint that posts something other than JSON must not start
// failing because this hop is in the path.
var legacyA2APrefixes = []string{"/api/a2a/", "/api/a2a-sandboxes/"}

// isLegacyA2APath reports whether a request is one of the invoke posts the
// pinned CLI gets wrong. The path is cleaned first so an equivalent spelling
// cannot walk a real send past the rewrite.
func isLegacyA2APath(requestPath string) bool {
	cleaned := path.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	for _, prefix := range legacyA2APrefixes {
		if strings.HasPrefix(cleaned+"/", prefix) {
			return true
		}
	}
	return false
}

// Options configure the compatibility hop.
type Options struct {
	// Upstream is the controller forward this hop serves, and the ONLY
	// place it will ever send a request. It must be a plaintext loopback
	// address with no path — the port-forward kmx opened itself.
	Upstream string
	// Log receives transport failures between the hop and the controller;
	// nil discards them (the caller still sees the HTTP status).
	Log io.Writer
	// NewID overrides the generated message ID (tests).
	NewID func() string
}

// Proxy is a loopback HTTP hop in front of one kagent controller forward.
//
// It is not a proxy in the HTTP sense and must not become one: the target is
// fixed when it is opened, it refuses proxy-form requests, and it ignores
// anything the client says about where a request should go.
type Proxy struct {
	base      string
	listener  net.Listener
	server    *http.Server
	reverse   *httputil.ReverseProxy
	transport *http.Transport
	newID     func() string
	closeOnce sync.Once
	closeErr  error
}

// Start opens the hop on a fresh loopback port.
func Start(opt Options) (*Proxy, error) {
	target, err := loopbackUpstream(opt.Upstream)
	if err != nil {
		return nil, err
	}
	logWriter := opt.Log
	if logWriter == nil {
		logWriter = io.Discard
	}
	newID := opt.NewID
	if newID == nil {
		newID = NewMessageID
	}
	// Bound to 127.0.0.1 explicitly. A hop that rewrites requests to a
	// cluster must not be reachable from the network, even briefly.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("cannot open the kagent compatibility hop: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy := &Proxy{
		base:      "http://" + listener.Addr().String(),
		listener:  listener,
		transport: transport,
		newID:     newID,
	}
	proxy.reverse = &httputil.ReverseProxy{
		// Rewrite (not Director) so Go strips hop-by-hop headers and does
		// not invent X-Forwarded-* on a hop that is not a proxy.
		Rewrite:   func(request *httputil.ProxyRequest) { request.SetURL(target) },
		Transport: transport,
		ErrorLog:  log.New(logWriter, "kagent compatibility hop: ", 0),
	}
	proxy.server = &http.Server{
		Handler: proxy,
		// No read or write deadline: an interactive turn is one long-lived
		// server-sent event stream. The header deadline still closes a
		// connection that never states its business.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

// URL is the base URL the pinned CLI must be given in place of the forward.
func (p *Proxy) URL() string { return p.base }

// Close releases the port. It is idempotent, because chat exits down several
// paths and every one of them closes the hop with the forward it fronts —
// a listener that outlived the tunnel would answer a later chat with a dead
// upstream.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.closeErr = p.server.Close()
		// Server.Close only reaches listeners Serve has already registered,
		// and Start hands this one over on a goroutine — so close it here
		// too rather than leave a window where the port is still accepting.
		if err := p.listener.Close(); err != nil && p.closeErr == nil && !errors.Is(err, net.ErrClosed) {
			p.closeErr = err
		}
		p.transport.CloseIdleConnections()
		if errors.Is(p.closeErr, net.ErrClosed) {
			p.closeErr = nil
		}
	})
	return p.closeErr
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// One fixed controller. A CONNECT tunnel or an absolute request URI is
	// how a listener like this turns into an open proxy, so both are refused
	// here rather than normalised away downstream.
	if r.Method == http.MethodConnect || r.URL.IsAbs() || r.URL.Host != "" {
		http.Error(w, "the kagent compatibility hop serves one fixed controller", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && isLegacyA2APath(r.URL.Path) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request body is past the compatibility hop's bound", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "request body could not be read", http.StatusBadRequest)
			return
		}
		rewritten, _, err := InjectMessageID(body, p.newID)
		if err != nil {
			// Fail closed. Forwarding bytes it could not read would mean
			// claiming a rewrite it never performed.
			http.Error(w, "request body is not a JSON-RPC call", http.StatusBadRequest)
			return
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(rewritten))
		r.ContentLength = int64(len(rewritten))
		r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		r.TransferEncoding = nil
	}
	p.reverse.ServeHTTP(w, r)
}

// loopbackUpstream accepts only the kind of address kmx opens for itself:
// plaintext HTTP, a loopback IP LITERAL (not a name that could resolve
// somewhere else), an explicit port, and no path to join onto.
func loopbackUpstream(upstream string) (*url.URL, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("kagent compatibility hop: upstream %q is not a URL: %w", upstream, err)
	}
	if target.Scheme != "http" {
		return nil, fmt.Errorf("kagent compatibility hop: upstream %q must be a plaintext loopback forward", upstream)
	}
	if target.Path != "" && target.Path != "/" {
		return nil, fmt.Errorf("kagent compatibility hop: upstream %q must name a host and port only", upstream)
	}
	host, port, err := net.SplitHostPort(target.Host)
	if err != nil || port == "" {
		return nil, fmt.Errorf("kagent compatibility hop: upstream %q must name an explicit port", upstream)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("kagent compatibility hop: upstream %q is not a loopback address", upstream)
	}
	return target, nil
}
