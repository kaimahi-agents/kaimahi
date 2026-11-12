package modelaccess

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilitiesMatchKnownUnknownAndMismatch(t *testing.T) {
	yes, no := true, false
	r := Requirements{ContextTokens: 8192, OutputTokens: 1024, Reasoning: true}
	for _, test := range []struct {
		cap  Capabilities
		want string
	}{
		{Capabilities{}, "unknown"},
		{Capabilities{ContextTokens: 8192, OutputTokens: 1024, Reasoning: &yes}, "match"},
		{Capabilities{ContextTokens: 4096, OutputTokens: 1024, Reasoning: &yes}, "mismatch"},
		{Capabilities{ContextTokens: 8192, OutputTokens: 1024, Reasoning: &no}, "mismatch"},
		{Capabilities{ContextTokens: 8192, Reasoning: &yes}, "unknown"},
	} {
		if got := r.Match(test.cap); got != test.want {
			t.Fatalf("got %s want %s", got, test.want)
		}
	}
}

func TestOutputBudgetPreservesRequirementsAndBoundsDefaults(t *testing.T) {
	for _, tc := range []struct {
		required, limit, want int
		refused               bool
	}{
		{0, 0, 1024, false}, {0, 512, 512, false}, {0, 4096, 1024, false},
		{2048, 4096, 2048, false}, {1024, 512, 0, true}, {2048, 0, 0, true},
	} {
		got, err := (Requirements{OutputTokens: tc.required}).OutputBudget(Capabilities{OutputTokens: tc.limit})
		if (err != nil) != tc.refused || got != tc.want {
			t.Fatalf("required=%d limit=%d: %d %v", tc.required, tc.limit, got, err)
		}
	}
}

func TestProfileRejectsAmbiguousAndUnboundDeclarations(t *testing.T) {
	for i, raw := range []string{
		`{"route":{"provider":"openai","model":"unit","endpoint":"https://api.openai.com/v1","auth":"env","keyEnv":"OPENAI_API_KEY"},"capabilities":{"contextTokens":8192,"reasoning":true}}`,
		`{"route":{"provider":"openai","model":"unit","endpoint":"https://api.openai.com/v1","auth":"env","keyEnv":"OPENAI_API_KEY"},"capabilities":{"contextTokens":8192,"contextTokens":8192}}`,
		`{"capabilities":{"contextTokens":8192}}`,
	} {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = LoadProfile(path)
		if (err == nil) != (i == 0) {
			t.Fatalf("unexpected result %v", err)
		}
	}
}

func TestOllamaAdvertisedCapabilitiesKeepOutputUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/show" {
			t.Error("unexpected probe")
		}
		io.WriteString(w, `{"capabilities":["completion","thinking","tools"],"model_info":{"unit.context_length":32768}}`)
	}))
	defer server.Close()
	c, err := OllamaCapabilities(t.Context(), Route{Provider: "ollama", Model: "unit", Endpoint: server.URL + "/v1", Auth: "none"})
	if err != nil || c.ContextTokens != 32768 || c.OutputTokens != 0 || c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestOllamaCapabilityCancellationPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := OllamaCapabilities(ctx, Route{Provider: "ollama", Model: "unit", Endpoint: "http://127.0.0.1:11434/v1", Auth: "none"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestOllamaHTTPSMetadataKeepsGatewayPrefixAndCredential(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != "/gateway/api/show" || r.Header.Get("Authorization") != "Bearer metadata-private-key" {
			t.Error("wrong selected metadata route or credential")
		}
		io.WriteString(w, `{"capabilities":["thinking"],"model_info":{"unit.context_length":32768}}`)
	}))
	defer server.Close()
	route := Route{Provider: "ollama", Model: "unit", Endpoint: server.URL + "/gateway/v1/", Auth: "env", KeyEnv: "OLLAMA_TEST_KEY"}
	access, err := NewEndpointAccess(route, "metadata-private-key", 0)
	if err != nil {
		t.Fatal(err)
	}
	access.next = server.Client().Transport
	c, err := ollamaCapabilities(t.Context(), access)
	if err != nil || c.ContextTokens != 32768 || c.Reasoning == nil || !*c.Reasoning || requests != 1 {
		t.Fatalf("capabilities=%+v requests=%d err=%v", c, requests, err)
	}
}

func TestOllamaMetadataRefusesRedirectAndCredentialEcho(t *testing.T) {
	for _, reply := range []string{"redirect", "echo"} {
		t.Run(reply, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if reply == "redirect" {
					w.Header().Set("Location", "/other")
					w.WriteHeader(302)
					return
				}
				io.WriteString(w, `{"capabilities":["metadata-private-key"]}`)
			}))
			defer server.Close()
			t.Setenv("OLLAMA_TEST_KEY", "metadata-private-key")
			_, err := OllamaCapabilities(t.Context(), Route{Provider: "ollama", Model: "unit", Endpoint: server.URL + "/v1", Auth: "env", KeyEnv: "OLLAMA_TEST_KEY"})
			if err == nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}
