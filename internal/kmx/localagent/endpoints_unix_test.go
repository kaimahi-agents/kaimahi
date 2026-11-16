//go:build !windows

package localagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if provider == "anthropic" {
						if r.URL.Path != "/v1/models/unit" || r.Header.Get("x-api-key") != "private-resident-canary" {
							t.Error("invalid Anthropic availability probe")
						}
						io.WriteString(w, `{"type":"model","id":"unit"}`)
					} else {
						if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer private-resident-canary" {
							t.Error("invalid availability probe")
						}
						io.WriteString(w, `{"data":[{"id":"unit"}]}`)
					}
					return
				}
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
			executable := residentTLSWorker(t, server)
			options := ActivationOptions{KeyEnv: "RESIDENT_ENDPOINT_KEY"}
			record, created, err := ActivateWithOptions(t.Context(), executable, pair, provider, server.URL+"/v1", "unit", options)
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
			if err := os.Unsetenv("RESIDENT_ENDPOINT_KEY"); err != nil {
				t.Fatal(err)
			}
			reused, created, err := ActivateWithOptions(t.Context(), executable, pair, provider, server.URL+"/v1", "unit", options)
			if err != nil || created || reused.InstanceID != record.InstanceID {
				t.Fatalf("exact reuse required credential: %+v %v", reused, err)
			}
			for _, mode := range []ActivationMode{ActivationNew, ActivationUpgrade} {
				changed := options
				changed.Mode = mode
				if _, _, err := ActivateWithOptions(t.Context(), executable, pair, provider, server.URL+"/v1", "unit", changed); err == nil {
					t.Fatalf("%s ignored missing credential", mode)
				}
			}
			answer, err = Invoke(t.Context(), record.InstanceID, session.ID, "retained key")
			if err != nil || answer != "hello endpoint" {
				t.Fatalf("worker lost credential snapshot: %q %v", answer, err)
			}
			t.Setenv("RESIDENT_ENDPOINT_KEY", "private-resident-canary")
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

func TestEndpointUpgradeProbeFailurePreservesWorkerAndSession(t *testing.T) {
	for _, failure := range []string{"unreachable", "unauthorized", "missing-model", "malformed", "unsupported"} {
		t.Run(failure, func(t *testing.T) {
			pair := residentAgentPair(t)
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, `{"data":[{"id":"unit"}]}`)
					return
				}
				io.WriteString(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"still working"}}]}`)
			}))
			defer good.Close()
			old, _, err := Activate(t.Context(), "", pair, "compatible", good.URL+"/v1", "unit")
			if err != nil {
				t.Fatal(err)
			}
			defer Stop(context.Background(), old.InstanceID)
			session, err := OpenSession(t.Context(), old.InstanceID, "kept")
			if err != nil {
				t.Fatal(err)
			}
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/models" {
					t.Error("readiness attempted inference or an unexpected route")
				}
				switch failure {
				case "unauthorized":
					w.WriteHeader(401)
				case "missing-model":
					io.WriteString(w, `{"data":[{"id":"other"}]}`)
				case "malformed":
					io.WriteString(w, `{"data":`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer bad.Close()
			if failure == "unreachable" {
				bad.Close()
			}
			if _, _, err := ActivateWithOptions(t.Context(), "", pair, "compatible", bad.URL+"/v1", "unit", ActivationOptions{Mode: ActivationUpgrade}); err == nil {
				t.Fatal("bad replacement promoted")
			}
			kept, err := ReadRecord(old.InstanceID)
			if err != nil || kept.State != "running" {
				t.Fatalf("old worker lost: %+v %v", kept, err)
			}
			answer, err := Invoke(t.Context(), old.InstanceID, session.ID, "continue")
			if err != nil || answer != "still working" {
				t.Fatalf("old session lost: %q %v", answer, err)
			}
		})
	}
}

func TestResidentTurnUsesFrozenOutputBudget(t *testing.T) {
	for _, tc := range []struct{ limit, required, want int }{{512, 0, 512}, {4096, 2048, 2048}} {
		t.Run(fmt.Sprintf("%d-%d", tc.limit, tc.required), func(t *testing.T) {
			pair := residentAgentPair(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, `{"data":[{"id":"unit"}]}`)
					return
				}
				var request struct {
					MaxTokens int `json:"max_tokens"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.MaxTokens != tc.want {
					t.Errorf("output budget %d want %d", request.MaxTokens, tc.want)
				}
				io.WriteString(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"bounded"}}]}`)
			}))
			defer server.Close()
			record, _, err := ActivateWithOptions(t.Context(), "", pair, "compatible", server.URL+"/v1", "unit", ActivationOptions{Capabilities: modelaccess.Capabilities{OutputTokens: tc.limit}, Requirements: modelaccess.Requirements{OutputTokens: tc.required}})
			if err != nil {
				t.Fatal(err)
			}
			defer Stop(context.Background(), record.InstanceID)
			session, err := OpenSession(t.Context(), record.InstanceID, "budget")
			if err != nil {
				t.Fatal(err)
			}
			if answer, err := Invoke(t.Context(), record.InstanceID, session.ID, "hi"); err != nil || answer != "bounded" {
				t.Fatalf("%q %v", answer, err)
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
