//go:build !windows

package localagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestResidentEndpointTurnAndRouteDrift(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "ollama", "foundry-local", "compatible"} {
		t.Run(provider, func(t *testing.T) {
			pair := residentAgentPair(t)
			t.Setenv("RESIDENT_ENDPOINT_KEY", "private-resident-canary")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider == "anthropic" {
					if r.Header.Get("x-api-key") != "private-resident-canary" {
						t.Error("missing key")
					}
					io.WriteString(w, `{"type":"message","role":"assistant","model":"unit","stop_reason":"end_turn","content":[{"type":"text","text":"hello endpoint"}]}`)
				} else {
					if r.Header.Get("Authorization") != "Bearer private-resident-canary" {
						t.Error("missing key")
					}
					io.WriteString(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hello endpoint"}}]}`)
				}
			}))
			defer server.Close()
			options := ActivationOptions{KeyEnv: "RESIDENT_ENDPOINT_KEY"}
			record, created, err := ActivateWithOptions(t.Context(), "", pair, provider, server.URL+"/v1", "unit", options)
			if err != nil || !created {
				t.Fatalf("activate %v", err)
			}
			defer Stop(context.Background(), record.InstanceID)
			session, err := OpenSession(t.Context(), record.InstanceID, "endpoint")
			if err != nil {
				t.Fatal(err)
			}
			answer, err := Invoke(t.Context(), record.InstanceID, session.ID, "hi")
			if err != nil || answer != "hello endpoint" {
				t.Fatalf("%q %v", answer, err)
			}
			_, _, err = ActivateWithOptions(t.Context(), "", pair, provider, server.URL+"/different", "unit", options)
			var drift *DriftError
			if !errors.As(err, &drift) {
				t.Fatalf("endpoint drift ignored: %v", err)
			}
			err = filepath.WalkDir(os.Getenv("KMX_HOME"), func(path string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if !d.IsDir() {
					b, e := os.ReadFile(path)
					if e != nil {
						return e
					}
					if strings.Contains(string(b), "private-resident-canary") {
						t.Error("credential persisted")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResidentUnknownCapabilityRefusesBeforeState(t *testing.T) {
	pair := residentAgentPair(t)
	_, _, err := ActivateWithOptions(t.Context(), "", pair, "fake", "", "fake", ActivationOptions{Requirements: modelaccess.Requirements{Reasoning: true}})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown allowed: %v", err)
	}
	if _, err = os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "local-agents")); !os.IsNotExist(err) {
		t.Fatal("state created on refusal")
	}
}

func TestResidentCredentialInRouteIdentityRefusesBeforeCaching(t *testing.T) {
	pair := residentAgentPair(t)
	t.Setenv("RESIDENT_ENDPOINT_KEY", "private-resident-canary")
	_, _, err := ActivateWithOptions(t.Context(), "", pair, "openai", "https://api.openai.com/v1", "private-resident-canary", ActivationOptions{KeyEnv: "RESIDENT_ENDPOINT_KEY"})
	if err == nil || strings.Contains(err.Error(), "private-resident-canary") {
		t.Fatalf("credential identity accepted or exposed: %v", err)
	}
	if _, err = os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "local-agents")); !os.IsNotExist(err) {
		t.Fatal("credential-bearing state created")
	}
}
