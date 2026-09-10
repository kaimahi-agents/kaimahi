package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// orkaResultSession owns one short-lived, in-memory bearer and proven loopback
// forward. There are no credential flags, persistent files, redirects or proxy
// environment inputs. The account's authority is not reduced by this session.
type orkaResultSession struct {
	token, base string
	client      *http.Client
	forward     *admin.Forward
	conn        net.Conn
	ctx         context.Context
	cancel      context.CancelFunc
}

// Losing this connection ends the session, including idle EOF and transport
// closes. Reconnecting would trust a new listener that may not be our forward.
type orkaResultConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *orkaResultConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.cancel()
	}
	return n, err
}

func (c *orkaResultConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		c.cancel()
	}
	return n, err
}

func (c *orkaResultConn) Close() error {
	c.cancel()
	return c.Conn.Close()
}

func (a *App) openOrkaResultSession(ctx context.Context, opt CreateOptions) (*orkaResultSession, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("result session requires a bounded execution deadline")
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", opt.Namespace, "get", "serviceaccount", opt.ResultServiceAccount, "--ignore-not-found=true", "-o", "name")
	if err != nil {
		return nil, fmt.Errorf("cannot read the selected result ServiceAccount: %w", err)
	}
	if strings.TrimSpace(string(raw)) != "serviceaccount/"+opt.ResultServiceAccount {
		return nil, fmt.Errorf("result ServiceAccount does not exist in the selected namespace")
	}
	raw, err = a.orkaCapture(ctx, nil, "-n", opt.Namespace, "get", "service", opt.OrkaAPIService, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read the selected Orka API Service: %w", err)
	}
	var service struct {
		Spec struct {
			Ports []struct {
				Port int `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &service); err != nil {
		return nil, fmt.Errorf("invalid Orka API Service response")
	}
	hasPort := false
	for _, port := range service.Spec.Ports {
		if port.Port == 8080 {
			hasPort = true
		}
	}
	if !hasPort {
		return nil, fmt.Errorf("Orka API Service must expose port 8080")
	}
	// orkaCapture never echoes commands, stdout or stderr, including failures.
	raw, err = a.orkaCapture(ctx, nil, "-n", opt.Namespace, "create", "token", opt.ResultServiceAccount, "--duration=10m", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("could not mint the temporary result session token: %w", err)
	}
	var request struct {
		Status struct {
			Token      string    `json:"token"`
			Expiration time.Time `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &request); err != nil || request.Status.Token == "" || strings.IndexFunc(request.Status.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, fmt.Errorf("API server returned an invalid temporary token response")
	}
	if request.Status.Expiration.Before(deadline.Add(30 * time.Second)) {
		return nil, fmt.Errorf("granted token lifetime does not cover the total execution deadline plus 30 seconds; refusing execution")
	}
	a.notef("Task execution uses the selected ServiceAccount's full effective authority. v0.1.3 authenticates result reads but does not enforce Task-read RBAC; discarding the token is not revocation.")
	forwardCtx, cancel := context.WithCancel(ctx)
	// The adapter's context is used only to prepare this forward process. Once
	// started, exec.CommandContext retains it; later kubectl calls use their own
	// request deadlines. This also cancels a forward still waiting to bind.
	a.orkaForwardContext = forwardCtx
	fwd, err := admin.StartForward(a, opt.Namespace, "svc/"+opt.OrkaAPIService, opt.ResultPort, "8080")
	a.orkaForwardContext = nil
	if err != nil {
		cancel()
		return nil, fmt.Errorf("Orka result port-forward did not prove a 127.0.0.1 bind; use a free result port and check Service access")
	}
	go func() {
		select {
		case <-fwd.Done():
			cancel()
		case <-forwardCtx.Done():
		}
	}()
	// Establish once, before exposing any authenticated request. Startup still
	// trusts kubectl's bind announcement and the initial local connection; this
	// is not TLS or cryptographic process identity. After this point a reused
	// port cannot receive the bearer: no transport dial opens another socket.
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(forwardCtx, "tcp", "127.0.0.1:"+opt.ResultPort)
	if err != nil {
		cancel()
		fwd.Close()
		return nil, fmt.Errorf("cannot establish the Orka result connection; no resources created")
	}
	bound := &orkaResultConn{Conn: conn, cancel: cancel}
	context.AfterFunc(forwardCtx, func() { _ = bound.Close() })
	select {
	case <-fwd.Done():
		cancel()
	default:
	}
	if forwardCtx.Err() != nil {
		_ = bound.Close()
		fwd.Close()
		return nil, fmt.Errorf("Orka result forward ended before the session opened")
	}
	var take sync.Once
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			var conn net.Conn
			take.Do(func() { conn = bound })
			if conn == nil {
				return nil, fmt.Errorf("Orka result connection lost; refusing to reconnect")
			}
			return conn, nil
		},
		MaxConnsPerHost:       1,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &orkaResultSession{
		token: request.Status.Token, base: "http://127.0.0.1:" + opt.ResultPort, forward: fwd, conn: bound, ctx: forwardCtx, cancel: cancel,
		client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (s *orkaResultSession) close() {
	if s == nil {
		return
	}
	s.cancel()
	_ = s.conn.Close()
	s.forward.Close()
	s.client.CloseIdleConnections()
	s.token = ""
}

func (s *orkaResultSession) get(ctx context.Context, namespace, name string) (int, map[string]json.RawMessage, error) {
	// Bind each caller's request to both its own deadline and the session's
	// lifetime. The pinned connection, not this liveness check, prevents redial.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case <-s.forward.Done():
		return 0, nil, fmt.Errorf("Orka result forward ended; refusing request")
	default:
	}
	if s.ctx.Err() != nil {
		return 0, nil, fmt.Errorf("Orka result session ended; refusing request")
	}
	// Names were validated before generation; redirects and HTTP_PROXY are
	// disabled, and the transport can only use the session's existing socket.
	endpoint := s.base + "/api/v1/tasks/" + url.PathEscape(name) + "/result?namespace=" + url.QueryEscape(namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("cannot construct result request")
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Orka result request failed or timed out")
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return response.StatusCode, nil, fmt.Errorf("Orka result endpoint returned non-JSON content (HTTP %d)", response.StatusCode)
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return response.StatusCode, nil, fmt.Errorf("Orka result response could not be read within the size limit")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return response.StatusCode, nil, fmt.Errorf("Orka result endpoint returned malformed JSON")
	}
	return response.StatusCode, envelope, nil
}

func orkaResultError(envelope map[string]json.RawMessage) (int, string, bool) {
	if len(envelope) != 1 {
		return 0, "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["error"], &fields); err != nil || len(fields) != 2 {
		return 0, "", false
	}
	var code int
	var message string
	if json.Unmarshal(fields["code"], &code) != nil || json.Unmarshal(fields["message"], &message) != nil {
		return 0, "", false
	}
	return code, message, true
}

func (s *orkaResultSession) probe(ctx context.Context, namespace, name string) error {
	status, envelope, err := s.get(ctx, namespace, name)
	if err != nil {
		return err
	}
	code, message, valid := orkaResultError(envelope)
	if status != http.StatusNotFound || !valid || code != 404 || message != "task not found" {
		return fmt.Errorf("Orka result access preflight refused (HTTP %d); expected the exact absent-Task JSON response. No resources created", status)
	}
	return nil
}

func (a *App) waitOrkaTaskResult(ctx context.Context, namespace string, id orkaIdentity, session *orkaResultSession) (answer string, err error) {
	succeeded := false
	defer func() {
		if err != nil {
			err = fmt.Errorf("Task %s/%s UID %s (execution succeeded: %t): %w; no execution retry or cleanup", namespace, id.Name, id.UID, succeeded, err)
		}
	}()
	for {
		object, err := a.readOrkaObject(ctx, namespace, id)
		if err != nil {
			return "", err
		}
		if object.Status.Phase == "Failed" || object.Status.Phase == "Cancelled" {
			return "", fmt.Errorf("execution ended in %s", object.Status.Phase)
		}
		succeeded = object.Status.Phase == "Succeeded"
		if succeeded && object.Status.ResultRef.Available {
			break
		}
		if err := orkaPause(ctx); err != nil {
			return "", err
		}
	}
	for {
		// Both checks surround the actual HTTP read, not merely the earlier poll.
		before, err := a.readOrkaObject(ctx, namespace, id)
		if err != nil {
			return "", err
		}
		if !orkaTaskSuccessful(before) {
			return "", fmt.Errorf("Task no longer has successful terminal state and available result")
		}
		status, envelope, err := session.get(ctx, namespace, id.Name)
		if err != nil {
			return "", err
		}
		retry := false
		switch status {
		case http.StatusOK:
			raw, exists := envelope["result"]
			if !exists {
				retry = true
			} else if string(raw) == "null" || json.Unmarshal(raw, &answer) != nil {
				return "", fmt.Errorf("Orka result must be a nonblank string")
			} else if strings.TrimSpace(answer) == "" {
				retry = true
			}
		case http.StatusNotFound:
			code, message, valid := orkaResultError(envelope)
			if !valid || code != 404 || (message != "task has no result" && message != "result not found") {
				return "", fmt.Errorf("unexpected missing Task/result response")
			}
			retry = true
		default:
			return "", fmt.Errorf("Orka result read refused (HTTP %d)", status)
		}
		after, err := a.readOrkaObject(ctx, namespace, id)
		if err != nil {
			return "", err
		}
		if !orkaTaskSuccessful(after) {
			return "", fmt.Errorf("Task changed successful terminal state during result retrieval")
		}
		if !retry {
			if strings.Contains(answer, session.token) {
				return "", fmt.Errorf("refusing result that echoes session credential material")
			}
			answer = safeTerminal(answer)
			// Removing control sequences can reconstruct a split credential.
			if strings.Contains(answer, session.token) {
				return "", fmt.Errorf("refusing result that echoes session credential material")
			}
			if strings.TrimSpace(answer) == "" {
				return "", fmt.Errorf("Orka result contains no printable answer")
			}
			return answer, nil
		}
		if err := orkaPause(ctx); err != nil {
			return "", fmt.Errorf("result remained unavailable: %w", err)
		}
	}
}

func orkaTaskSuccessful(object *orkaObject) bool {
	return object.Status.Phase == "Succeeded" && object.Status.ResultRef.Available
}
