package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Qualification is explicit and version-pinned; ordinary runs do not download.
func TestDiscoveryTargetPinnedKubectl(t *testing.T) {
	binary := os.Getenv("KMX_TEST_KUBECTL")
	if binary == "" {
		t.Skip("set exact staged v1.37.0 client")
	}
	raw, e := exec.Command(binary, "version", "--client", "-ojson").Output()
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		ClientVersion struct{ GitVersion, GitCommit string }
	}
	if json.Unmarshal(raw, &v) != nil || v.ClientVersion.GitVersion != "v1.37.0" || v.ClientVersion.GitCommit != "f54c212e3a2f75d674b717a9b29052b20b60aefc" {
		t.Fatal("wrong managed client")
	}
	_, pages := targetFixture(t)
	var denied, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Error("mutation", r.Method)
			w.WriteHeader(500)
			return
		}
		q := r.URL.Query()
		if q.Get("timeout") != "10s" {
			t.Error("unbounded request", r.URL)
		}
		q.Del("timeout")
		path := r.URL.Path
		if q.Encode() != "" {
			path += "?" + q.Encode()
		}
		value, exists := pages[path]
		if !exists || (denied.Load() == 1 && strings.Contains(path, "/tools?")) {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "Forbidden", "message": "Forbidden", "code": 403})
			if !exists {
				t.Error("unexpected GET", path)
			}
			return
		}
		mode := denied.Load()
		if mode == 2 && strings.Contains(path, "/providers?") {
			value = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "ProviderList", "metadata": map[string]any{"resourceVersion": "1"}, "items": []any{}}
		}
		if mode == 3 && path == "/apis/core.orka.ai/v1alpha1" {
			value = map[string]any{"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": "core.orka.ai/v1alpha1", "resources": []any{map[string]any{"name": "tools", "kind": "Tool", "namespaced": true, "verbs": []string{"get", "list"}}}}
		}
		if e := json.NewEncoder(w).Encode(value); e != nil {
			t.Error(e)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	if e := os.Symlink(binary, filepath.Join(dir, "kubectl")); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "")
	}
	cfg := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "must-not-select", "contexts": []any{map[string]any{"name": "kind-test", "context": map[string]any{"cluster": "fixture"}}}, "clusters": []any{map[string]any{"name": "fixture", "cluster": map[string]any{"server": server.URL}}}, "users": []any{}}
	raw, _ = json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("KUBECONFIG", path)
	t.Setenv("KUBE_CTX", "must-not-select")
	t.Setenv("KUBERC", filepath.Join(dir, "invalid-preferences"))
	t.Setenv("KUBECTL_KUBERC", "true")
	home := filepath.Join(dir, "no-state")
	t.Setenv("KMX_HOME", home)
	a := captureTestApp()
	for _, mode := range []int32{0, 1, 2, 3} {
		denied.Store(mode)
		r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
		complete := mode == 0 || mode == 2
		exit := 0
		if !complete {
			exit = 1
		}
		if complete && (e != nil || !r.Complete) {
			t.Fatal(r, e)
		}
		if mode == 1 && (e == nil || r.Complete || r.Target.Tools == nil || r.Target.Tools.Complete) {
			t.Fatal(r, e)
		}
		if mode == 2 && len(r.Target.Providers) != 0 {
			t.Fatal(r.Target.Providers)
		}
		if mode == 3 && (e == nil || r.Complete) {
			t.Fatal("API refusal is not empty", r)
		}
		if cli := os.Getenv("KMX_TEST_BINARY"); cli != "" {
			cmd := exec.Command(cli, "cluster", "discover", "--context=kind-test", "--namespace=orka-system", "-ojsonl")
			out, ce := cmd.Output()
			if (ce == nil) != complete {
				t.Fatal(ce)
			}
			d := json.NewDecoder(strings.NewReader(string(out)))
			var frame map[string]any
			if d.Decode(&frame) != nil || frame["operation"] != "cluster.discover" || frame["exitCode"] != float64(exit) {
				t.Fatal(string(out))
			}
			if e := d.Decode(&frame); e != io.EOF {
				t.Fatal("extra frame", e)
			}
			t.Logf("compiled CLI mode=%d exit=%d, one terminal", mode, exit)
		}
	}
	if _, e := os.Stat(home); !os.IsNotExist(e) {
		t.Fatal("state written", e)
	}
	if requests.Load() < 20 {
		t.Fatal("missing composed GET trace", requests.Load())
	}
	t.Logf("owned loopback GETs=%d, no Namespace LIST or mutations", requests.Load())
}
