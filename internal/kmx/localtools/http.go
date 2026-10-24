package localtools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// mcpHTTP bounds the external protocol before the SDK decodes or logs it. It
// also binds SDK-detached cleanup requests to the read's original deadline.
type mcpHTTP struct {
	base http.RoundTripper
	host *Host
	ctx  context.Context
	mu   sync.Mutex
	used map[string]bool
}

func (t *mcpHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.ctx.Err() != nil || req.URL == nil || req.URL.String() != endpointURL {
		return nil, errReadFailed
	}
	method := "DELETE"
	var data []byte
	if req.Method == http.MethodPost {
		if req.Body == nil {
			return nil, errReadFailed
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(req.Body, 8193))
		req.Body.Close()
		if err != nil || len(data) > 8192 || !agentruntime.JSONKeysUnique(data) {
			return nil, errReadFailed
		}
		var call struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(data, &call) != nil {
			return nil, errReadFailed
		}
		method = call.Method
		switch method {
		case "initialize", "notifications/initialized", "tools/list", "tools/call":
		default:
			return nil, errReadFailed
		}
	} else if req.Method != http.MethodDelete {
		return nil, errReadFailed
	}
	t.mu.Lock()
	used := t.used[method]
	t.used[method] = true
	t.mu.Unlock()
	if used {
		return nil, errReadFailed
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	defer func() { stop(); cancel() }()
	copy := req.Clone(ctx)
	copy.Header.Set("Authorization", "Bearer "+t.host.credential)
	// Disable net/http body replay as well as the SDK's reconnect/MRT retries.
	copy.GetBody = nil
	if req.Method == http.MethodPost {
		copy.Body = io.NopCloser(bytes.NewReader(data))
	}
	response, err := t.base.RoundTrip(copy)
	if err != nil {
		return nil, errReadFailed
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 202 && response.StatusCode != 204 {
		return nil, errReadFailed
	}
	headerBytes := 0
	for key, values := range response.Header {
		headerBytes += len(key)
		for _, value := range values {
			headerBytes += len(value)
			if strings.Contains(key, t.host.credential) || strings.Contains(value, t.host.credential) {
				return nil, errReadFailed
			}
		}
	}
	if headerBytes > 8192 {
		return nil, errReadFailed
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxMCPBytes+1))
	if err != nil || len(raw) > maxMCPBytes || strings.Contains(string(raw), t.host.credential) {
		return nil, errReadFailed
	}
	if len(raw) > 0 {
		contentType := strings.Split(response.Header.Get("Content-Type"), ";")[0]
		switch contentType {
		case "application/json":
			if !safeMCPJSON(raw, t.host) {
				return nil, errReadFailed
			}
		case "text/event-stream":
			blocks := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n\n")
			if len(blocks) > 33 {
				return nil, errReadFailed
			}
			messages := 0
			for _, block := range blocks {
				var lines []string
				for _, line := range strings.Split(block, "\n") {
					if strings.HasPrefix(line, "data:") {
						lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
					}
				}
				if data := strings.Join(lines, "\n"); data != "" {
					// Legacy MCP SSE may prime the cursor with an empty data event.
					messages++
					if !safeMCPJSON([]byte(data), t.host) {
						return nil, errReadFailed
					}
				}
			}
			if messages != 1 {
				return nil, errReadFailed
			}
		default:
			return nil, errReadFailed
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}

func safeMCPJSON(raw []byte, h *Host) bool {
	// The entire protocol stays host-side. Check exact configured-credential
	// echoes here, not credential-shaped opaque icons/session metadata. Known
	// shapes are still refused by Host.Check on all model/journal-bound data.
	if !agentruntime.JSONKeysUnique(raw) || strings.Contains(string(raw), h.credential) {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var check func(any) bool
	check = func(value any) bool {
		switch v := value.(type) {
		case string:
			return !strings.Contains(v, h.credential)
		case []any:
			for _, item := range v {
				if !check(item) {
					return false
				}
			}
		case map[string]any:
			for key, item := range v {
				if strings.Contains(key, h.credential) || !check(item) {
					return false
				}
			}
		}
		return true
	}
	if !check(value) {
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, callback := object["method"]; callback {
		return false
	}
	if result, ok := object["result"].(map[string]any); ok {
		if version, present := result["protocolVersion"]; present && version != mcpProtocol {
			return false
		}
		// Newer multi-round-trip results must never cause implicit resends, even if
		// a peer sends them while the legacy protocol is selected.
		if kind, present := result["resultType"]; present && kind != "" && kind != "complete" {
			return false
		}
		if state, present := result["requestState"]; present && state != "" {
			return false
		}
		if requests, present := result["inputRequests"]; present && requests != nil {
			if m, ok := requests.(map[string]any); !ok || len(m) != 0 {
				return false
			}
		}
	}
	return true
}
