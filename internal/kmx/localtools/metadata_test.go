package localtools

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Public icon data can match a credential heuristic. It is host-only protocol
// metadata, not content allowed into the model or journal.
func TestMCPHostDoesNotForwardOpaqueServerMetadata(t *testing.T) {
	for _, exactCredential := range []bool{false, true} {
		t.Run(map[bool]string{false: "opaque icon", true: "actual credential"}[exactCredential], func(t *testing.T) {
			f := newMCPFixture(t, mergedSearch)
			marker := strings.Repeat("Aa1b", 16) + "JQQJ" + strings.Repeat("c9D", 16)
			if exactCredential {
				marker = testMCPCredential
			}
			wrapped := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var raw []byte
				if r.Body != nil {
					raw, _ = io.ReadAll(r.Body)
					r.Body = io.NopCloser(strings.NewReader(string(raw)))
				}
				res, err := f.Transport.RoundTrip(r)
				if err == nil && strings.Contains(string(raw), `"method":"initialize"`) {
					data, _ := io.ReadAll(res.Body)
					res.Body.Close()
					var v map[string]any
					_ = json.Unmarshal(data, &v)
					v["result"].(map[string]any)["serverInfo"].(map[string]any)["icons"] = []any{map[string]any{"src": "data:image/png;base64," + marker, "mimeType": "image/png"}}
					data, _ = json.Marshal(v)
					res.Body = io.NopCloser(strings.NewReader(string(data)))
				}
				return res, err
			})
			h, err := newMCPHost(Binding{"owner/repo", endpointURL, "TEST_MCP_TOKEN"}, testMCPCredential, wrapped)
			if err != nil {
				t.Fatal(err)
			}
			result, err := executeMCP(t.Context(), h)
			if exactCredential {
				if err == nil || f.Calls.Load() != 0 {
					t.Fatal("credential echo accepted")
				}
				return
			}
			if err != nil || f.Calls.Load() != 1 {
				t.Fatal("opaque non-model metadata refused")
			}
			serialized, _ := json.Marshal(result.Output)
			if strings.Contains(string(serialized), marker) {
				t.Fatal("server metadata forwarded")
			}
		})
	}
}
