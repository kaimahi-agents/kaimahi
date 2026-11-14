package modelaccess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCredentialedHTTPRefusesBeforeAnyRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, endpoint := range []string{server.URL + "/v1", strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/v1", "http://[::1]:11434/v1"} {
		route := Route{Provider: "ollama", Model: "unit", Endpoint: endpoint, Auth: "env", KeyEnv: "MODEL_TEST_KEY"}
		if _, err := NewEndpointAccess(route, "private-http-canary", 0); err == nil {
			t.Fatalf("credentialed HTTP accepted: %s", endpoint)
		}
		t.Setenv("MODEL_TEST_KEY", "private-http-canary")
		if _, err := OllamaCapabilities(t.Context(), route); err == nil {
			t.Fatal("credentialed metadata accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("credentialed HTTP performed a request")
	}
	if _, err := NewEndpointAccess(Route{Provider: "compatible", Model: "unit", Endpoint: server.URL + "/v1", Auth: "none"}, "", 0); err != nil {
		t.Fatalf("keyless loopback refused: %v", err)
	}
}

func TestEndpointLengthBound(t *testing.T) {
	route := Route{Provider: "compatible", Model: "unit", Endpoint: "https://example.test/", Auth: "none"}
	route.Endpoint += strings.Repeat("x", MaxEndpointBytes-len(route.Endpoint))
	if err := route.Validate(); err != nil {
		t.Fatal(err)
	}
	route.Endpoint += "x"
	if err := route.Validate(); err == nil {
		t.Fatal("oversized endpoint accepted")
	}
}

func TestRelativeProfilePathsPreserveCheckedReads(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	profile := Profile{Route: Route{Provider: "openai", Model: "unit", Endpoint: "https://api.openai.com/v1", Auth: "env", KeyEnv: "OPENAI_API_KEY"}, Capabilities: Capabilities{ContextTokens: 8192}}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("model.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"model.json", "./model.json", filepath.Join(root, "model.json")} {
		got, err := LoadProfile(path)
		if err != nil || got.Route != profile.Route || got.Capabilities.ContextTokens != 8192 {
			t.Fatalf("path %q: %+v %v", path, got, err)
		}
	}
}
