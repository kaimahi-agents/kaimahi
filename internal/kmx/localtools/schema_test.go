package localtools

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMCPRemoteSchemaCannotChangeTheFixedRead(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "required", "projection", "cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newMCPFixture(t, mergedSearch)
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var raw []byte
				if r.Body != nil {
					raw, _ = io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(string(raw)))
				}
				res, err := f.Transport.RoundTrip(r)
				if err == nil && strings.Contains(string(raw), `"method":"tools/list"`) {
					data, _ := io.ReadAll(res.Body)
					res.Body.Close()
					var root map[string]any
					_ = json.Unmarshal(data, &root)
					result := root["result"].(map[string]any)
					tools := result["tools"].([]any)
					switch mode {
					case "missing":
						result["tools"] = []any{}
					case "duplicate":
						result["tools"] = append(tools, tools[0])
					case "required":
						tools[0].(map[string]any)["inputSchema"].(map[string]any)["required"] = []string{"query", "write"}
					case "projection":
						delete(tools[0].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any), "fields")
					case "cursor":
						result["nextCursor"] = "another-page"
					}
					data, _ = json.Marshal(root)
					res.Body = io.NopCloser(strings.NewReader(string(data)))
				}
				return res, err
			})
			h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, transport)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = executeMCP(t.Context(), h); err == nil || f.Calls.Load() != 0 {
				t.Fatal("unqualified schema dispatched a read")
			}
		})
	}
}
