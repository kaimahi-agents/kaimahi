package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Real kubectl performs discovery and REST mapping against a local, keyless API.
// A fake executable matching argv alone cannot reproduce same-kind ambiguity.
func TestLegacyAgentsWithOrkaDiscovery(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("real kubectl required for the dual-discovery regression (required in CI)")
	}
	var wrongGroup, legacyReads, legacyPatches atomic.Int32
	object := map[string]any{
		"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent",
		"metadata": map[string]any{"name": "hello-world", "namespace": "kagent", "uid": "fixture-agent", "generation": 1, "resourceVersion": "1"},
		"spec":     map[string]any{"type": "Declarative", "declarative": map[string]any{"modelConfig": "local"}},
		"status":   map[string]any{"observedGeneration": 1, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = map[string]any{"kind": "APIVersions", "apiVersion": "v1", "versions": []string{"v1"}}
		case "/api/v1":
			body = map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1", "resources": []any{}}
		case "/apis":
			var groups []any
			for _, group := range []struct{ name, version string }{{"core.orka.ai", "v1alpha1"}, {"kagent.dev", "v1alpha2"}} {
				version := map[string]string{"groupVersion": group.name + "/" + group.version, "version": group.version}
				groups = append(groups, map[string]any{"name": group.name, "versions": []any{version}, "preferredVersion": version})
			}
			body = map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": groups}
		case "/apis/core.orka.ai/v1alpha1", "/apis/kagent.dev/v1alpha2":
			body = map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": strings.TrimPrefix(r.URL.Path, "/apis/"), "resources": []any{map[string]any{"name": "agents", "singularName": "agent", "namespaced": true, "kind": "Agent", "verbs": []string{"get", "list", "watch", "patch"}}}}
		case "/apis/kagent.dev/v1alpha2/namespaces/kagent/agents":
			legacyReads.Add(1)
			body = map[string]any{"apiVersion": "kagent.dev/v1alpha2", "kind": "AgentList", "metadata": map[string]string{"resourceVersion": "1"}, "items": []any{object}}
		case "/apis/kagent.dev/v1alpha2/namespaces/kagent/agents/hello-world":
			legacyReads.Add(1)
			if r.Method == http.MethodPatch {
				legacyPatches.Add(1)
			}
			body = object
		default:
			if strings.HasPrefix(r.URL.Path, "/apis/core.orka.ai/v1alpha1/namespaces/") {
				wrongGroup.Add(1)
			}
			w.WriteHeader(http.StatusNotFound)
			body = map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404, "message": "fixture resource not found"}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	kubeconfig := filepath.Join(t.TempDir(), "config")
	data := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: %s\ncontexts:\n- name: kind-dual-discovery\n  context:\n    cluster: fixture\ncurrent-context: kind-dual-discovery\n", server.URL)
	if err := os.WriteFile(kubeconfig, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	var out, diagnostics bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-dual-discovery"}, Run: &run.Runner{Stdout: &out, Stderr: &diagnostics}, Out: &out, Err: &diagnostics}
	// Prove that this discovery order really exposes the old failure, rather
	// than relying on an API fixture that would accept either resource name.
	if _, err := a.kubectlCapture("-n", "kagent", "get", "agent", "hello-world", "-o", "name"); err == nil || wrongGroup.Load() != 1 {
		t.Fatalf("fixture did not expose unqualified discovery ambiguity: %v", err)
	}
	wrongGroup.Store(0)
	if err := a.ensureAgentExists("hello-world"); err != nil {
		t.Fatal(err)
	}
	if model, err := a.liveModelConfig("hello-world"); err != nil || model != "local" {
		t.Fatalf("legacy model read: %q, %v", model, err)
	}
	if err := a.patchModelConfig("hello-world", "local"); err != nil {
		t.Fatal(err)
	}
	if err := a.waitAgentReady("hello-world"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.ListAgents("json"); err != nil || !strings.Contains(out.String(), "hello-world") {
		t.Fatalf("legacy list failed: %v", err)
	}
	if wrongGroup.Load() != 0 || legacyReads.Load() < 4 || legacyPatches.Load() != 1 {
		t.Fatalf("wrong runtime selected: Orka=%d kagent=%d patches=%d", wrongGroup.Load(), legacyReads.Load(), legacyPatches.Load())
	}
}
