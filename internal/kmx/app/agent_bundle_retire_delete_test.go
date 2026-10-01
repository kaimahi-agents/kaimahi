package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// This test API rejects a deletion unless both Kubernetes DeleteOptions
// preconditions match the currently stored object. It never exposes tokens.
func setupRetireDeleteTestAPI(t *testing.T, dir string) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		prefix := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/"
		if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		tail := strings.TrimPrefix(r.URL.Path, prefix)
		kind, name, ok := strings.Cut(tail, "/")
		if !ok || name != "sample" || kind != "agents" && kind != "providers" {
			http.Error(w, "invalid resource", http.StatusNotFound)
			return
		}
		var options struct {
			Preconditions struct {
				UID             string `json:"uid"`
				ResourceVersion string `json:"resourceVersion"`
			} `json:"preconditions"`
		}
		if json.NewDecoder(r.Body).Decode(&options) != nil {
			http.Error(w, "invalid options", http.StatusBadRequest)
			return
		}
		path := filepath.Join(dir, kind+".core.orka.ai.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var obj struct {
			Metadata struct {
				UID             string `json:"uid"`
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &obj) != nil || options.Preconditions.UID != obj.Metadata.UID || options.Preconditions.ResourceVersion != obj.Metadata.ResourceVersion {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if os.Getenv("KMX_RETIRE_PERSIST_DELETE_KIND") == kind+".core.orka.ai" {
			w.WriteHeader(http.StatusAccepted)
		} else {
			if err := os.Remove(path); err != nil {
				http.Error(w, "write failed", http.StatusInternalServerError)
				return
			}
		}
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Success"}`))
	}))
	t.Cleanup(server.Close)
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: kind-test\n  cluster:\n    server: %s\n- name: ambient\n  cluster:\n    server: http://127.0.0.1:1\ncontexts:\n- name: kind-test\n  context:\n    cluster: kind-test\n    user: anonymous\n- name: ambient\n  context:\n    cluster: ambient\n    user: anonymous\ncurrent-context: ambient\nusers:\n- name: anonymous\n  user: {}\n", server.URL)
	configPath := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(configPath, []byte(kubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", configPath)
	return &requests
}

func TestRetireDeleteSendsBothAPIPreconditionsWithoutProxy(t *testing.T) {
	adapter, rendered, dir := reconcileFixture(t)
	live := reconcileLive(t, dir, "Agent", rendered)
	seedReconcile(t, dir, live)
	requests := setupRetireDeleteTestAPI(t, dir)
	id := retireDecision{kind: "Agent", name: "sample", uid: "wrong-uid", version: "1"}
	if err := adapter.app.deleteRetireObject(context.Background(), OrkaNamespace, id); err == nil {
		t.Fatal("wrong UID accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.core.orka.ai.json")); err != nil {
		t.Fatalf("UID mismatch deleted Agent: %v", err)
	}
	id.uid = "agent-uid"
	id.version = "wrong-version"
	if err := adapter.app.deleteRetireObject(context.Background(), OrkaNamespace, id); err == nil {
		t.Fatal("wrong version accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.core.orka.ai.json")); err != nil {
		t.Fatalf("version mismatch deleted Agent: %v", err)
	}
	id.version = "1"
	if err := adapter.app.deleteRetireObject(context.Background(), OrkaNamespace, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.core.orka.ai.json")); !os.IsNotExist(err) {
		t.Fatalf("matching preconditions did not delete: %v", err)
	}
	if requests.Load() != 3 {
		t.Errorf("expected three direct API requests; saw %d (proxy or retries used)", requests.Load())
	}
	for _, call := range orkaCalls(t, dir) {
		for _, arg := range call.Args {
			if arg == "proxy" {
				t.Fatal("kubectl proxy listener still used")
			}
		}
	}
}
