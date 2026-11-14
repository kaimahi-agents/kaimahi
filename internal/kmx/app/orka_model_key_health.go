package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/portforward"
)

// kubectl raw GET discards non-2xx bodies. Only after failed readiness, read
// Vekil's diagnostic privately through a forward to the observed gateway Pod.
// This second observation can classify a failure but can never establish ready.
func (a *App) modelKeyReadinessFailure(ctx context.Context, c OrkaModelConnection, children []modelChild) (err error) {
	unavailable := func() error { return modelFailure("dependency_unavailable", ctx.Err()) }
	pod := ""
	for _, child := range children {
		if child.Kind == "Pod" {
			if pod != "" {
				return unavailable()
			}
			pod = child.Name
		}
	}
	if pod == "" {
		return unavailable()
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker := *a
	worker.orkaForwardContext = forwardCtx
	fwd, err := portforward.Start(&worker, OrkaNamespace, "pod/"+pod, "", "1337")
	if err != nil {
		if errors.Is(err, portforward.ErrCleanupUnconfirmed) {
			return modelFailure("execution_unknown", nil)
		}
		return modelFailure("dependency_unavailable", err)
	}
	defer func() {
		if cleanup := fwd.Close(); cleanup != nil {
			err = modelFailure("execution_unknown", nil)
		}
	}()
	go func() {
		select {
		case <-fwd.Done():
			cancel()
		case <-forwardCtx.Done():
		}
	}()
	requestCtx, stop := context.WithTimeout(forwardCtx, 15*time.Second)
	defer stop()
	transport := &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://127.0.0.1:"+fwd.Port+"/readyz", nil)
	if e != nil {
		return unavailable()
	}
	a.setUserAgent(req)
	response, e := client.Do(req)
	if e != nil {
		return unavailable()
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if e != nil || len(raw) > 32<<10 || response.StatusCode != http.StatusServiceUnavailable {
		return unavailable()
	}
	var ready struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if modelJSON(raw, &ready) != nil || ready.Status != "not_ready" {
		return unavailable()
	}
	if ctx.Err() != nil {
		return unavailable()
	}
	select {
	case <-fwd.Done():
		return unavailable()
	default:
	}
	prefix := "provider \"" + c.Vendor + "\" upstream probe "
	for _, status := range []string{"401", "403"} {
		head := prefix + "returned " + status
		if ready.Error == head || strings.HasPrefix(ready.Error, head+": ") {
			return modelFailure("credential_rejected", nil)
		}
	}
	if strings.HasPrefix(ready.Error, prefix+"failed: ") {
		return modelFailure("provider_unreachable", nil)
	}
	return unavailable()
}
