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

func TestProfileRejectsAmbiguousAndUnboundDeclarations(t *testing.T) {
	for i, raw := range []string{
		`{"route":{"provider":"openai","model":"unit","endpoint":"https://api.openai.com/v1","auth":"env","keyEnv":"OPENAI_API_KEY"},"capabilities":{"contextTokens":8192,"reasoning":true}}`,
		`{"route":{"provider":"openai","model":"unit","endpoint":"https://api.openai.com/v1","auth":"env","keyEnv":"OPENAI_API_KEY"},"capabilities":{"contextTokens":8192,"contextTokens":8192}}`,
		`{"capabilities":{"contextTokens":8192}}`,
	} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadProfile(path)
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
