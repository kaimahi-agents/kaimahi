package localtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMCPResultEnvelopeRefusesErrorsAndAmbiguity(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"error rows":          func(v map[string]any) { v["isError"] = true },
		"structured conflict": func(v map[string]any) { v["structuredContent"] = map[string]any{"other": "data"} },
		"multiple text": func(v map[string]any) {
			v["content"] = []any{map[string]any{"type": "text", "text": mergedSearch}, map[string]any{"type": "text", "text": mergedSearch}}
		},
		"input required": func(v map[string]any) {
			v["resultType"] = "input_required"
			v["requestState"] = "state"
			v["inputRequests"] = map[string]any{"sample": map[string]any{"type": "sampling"}}
		},
		"state only": func(v map[string]any) { v["requestState"] = "state" },
		"credential": func(v map[string]any) {
			v["content"] = []any{map[string]any{"type": "text", "text": strings.Replace(mergedSearch, "Fix parser", testMCPCredential, 1)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMCPFixture(t, mergedSearch)
			wrapped := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var raw []byte
				if r.Body != nil {
					raw, _ = io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(string(raw)))
				}
				res, err := f.Transport.RoundTrip(r)
				if err == nil && strings.Contains(string(raw), `"method":"tools/call"`) {
					body, _ := io.ReadAll(res.Body)
					res.Body.Close()
					var value map[string]any
					if json.Unmarshal(body, &value) != nil {
						t.Error("bad fixture")
					}
					mutate(value["result"].(map[string]any))
					changed, _ := json.Marshal(value)
					res.Body = io.NopCloser(strings.NewReader(string(changed)))
				}
				return res, err
			})
			h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, wrapped)
			if err != nil {
				t.Fatal(err)
			}
			_, err = executeMCP(t.Context(), h)
			if err == nil || f.Calls.Load() != 1 || strings.Contains(err.Error(), testMCPCredential) {
				t.Fatal("unsafe envelope accepted or repeated")
			}
		})
	}
}

func TestMCPTransportRejectsUnboundDestinationAndMethod(t *testing.T) {
	var sends atomic.Int64
	base := roundTripFunc(func(*http.Request) (*http.Response, error) { sends.Add(1); return nil, io.EOF })
	h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, base)
	if err != nil {
		t.Fatal(err)
	}
	guard := &mcpHTTP{base: base, host: h, ctx: t.Context(), used: map[string]bool{}}
	for _, tc := range []struct{ method, url, body string }{
		{"GET", endpointURL, ""},
		{"POST", "https://foreign.invalid/", `{"method":"initialize"}`},
		{"POST", endpointURL, `{"method":"server/discover"}`},
		{"POST", endpointURL, `{"method":"sampling/createMessage"}`},
	} {
		req, _ := http.NewRequestWithContext(context.Background(), tc.method, tc.url, strings.NewReader(tc.body))
		if _, err := guard.RoundTrip(req); err == nil {
			t.Fatal("unbound request accepted")
		}
	}
	if sends.Load() != 0 {
		t.Fatal("unbound HTTP escaped")
	}
}

func TestMCPSSECredentialAndEventBounds(t *testing.T) {
	h, err := NewHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, func(context.Context) ([]PullRequest, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"valid":              "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
		"too many":           strings.Repeat(": heartbeat\n\n", 33) + "data: {\"id\":1,\"result\":{}}\n\n",
		"priming":            "event: prime\nid: cursor\ndata: \n\nevent: message\ndata: {\"id\":1,\"result\":{}}\n\n",
		"callback":           "data: {\"method\":\"roots/list\",\"id\":1}\n\n",
		"encoded credential": `data: {"id":1,"result":{"value":"\u006dcp-credential-canary"}}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			base := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			guard := &mcpHTTP{base: base, host: h, ctx: t.Context(), used: map[string]bool{}}
			req, _ := http.NewRequest("POST", endpointURL, strings.NewReader(`{"method":"initialize"}`))
			_, err := guard.RoundTrip(req)
			if (err == nil) != (name == "valid" || name == "priming") {
				t.Fatal("wrong SSE acceptance")
			}
		})
	}
}
