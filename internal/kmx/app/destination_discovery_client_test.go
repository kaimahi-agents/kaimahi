package app

import (
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
)

type discoveryGolden struct {
	ClientVersion struct{ GitVersion, GitCommit string }
	Rows          []struct {
		Status         int
		ExitCode       int
		Stdout, Stderr string
	}
	Network struct{ Stderr string }
}

func TestDiscoveryRecordedFixtures(t *testing.T) {
	for _, version := range []string{"v1.35.1", "v1.37.0"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "destination-discovery", version+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var golden discoveryGolden
		if err := json.Unmarshal(raw, &golden); err != nil {
			t.Fatal(err)
		}
		if golden.ClientVersion.GitVersion != version || len(golden.ClientVersion.GitCommit) != 40 || len(golden.Rows) != 3 {
			t.Fatal(golden)
		}
		for i, want := range []string{"unauthorized", "read_denied", "resource_missing"} {
			row := golden.Rows[i]
			if row.ExitCode != 1 || row.Stdout != "" || string(classifyDiscoveryRead([]byte(row.Stderr), false)) != want {
				t.Fatal(version, row)
			}
		}
		if classifyDiscoveryRead([]byte(golden.Network.Stderr), false) != "network_unavailable" {
			t.Fatal(version)
		}
	}
}

// Explicit developer qualification, not an ordinary test's implicit download or
// dependence on a cluster. A skipped run is not managed-client qualification.
func TestDiscoveryPinnedKubectl(t *testing.T) {
	binary := os.Getenv("KMX_TEST_KUBECTL")
	if binary == "" {
		t.Skip("set KMX_TEST_KUBECTL to the exact staged managed client")
	}
	raw, err := exec.Command(binary, "version", "--client", "-o", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		ClientVersion struct{ GitVersion, GitCommit string }
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		t.Fatal(err)
	}
	if version.ClientVersion.GitVersion != "v1.37.0" || version.ClientVersion.GitCommit != "f54c212e3a2f75d674b717a9b29052b20b60aefc" {
		t.Fatal("wrong managed client", version)
	}
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "kubectl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	var status, requests atomic.Int32
	status.Store(200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces" {
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		s := int(status.Load())
		if s == 200 {
			fmt.Fprint(w, discoveryPage("dev"))
			return
		}
		reason := map[int]string{401: "Unauthorized", 403: "Forbidden", 404: "NotFound"}[s]
		w.WriteHeader(s)
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": reason, "message": reason, "code": s})
	}))
	defer server.Close()
	configPath := filepath.Join(dir, "config.json")
	name := "arn:aws:eks:cluster/dev"
	names := []string{name, `quote"name`, "日本語", "bad\x01name"}
	contexts := []any{}
	for _, n := range names {
		contexts = append(contexts, map[string]any{"name": n, "context": map[string]any{"cluster": "fixture"}})
	}
	cfg := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": name, "contexts": contexts, "clusters": []any{map[string]any{"name": "fixture", "cluster": map[string]any{"server": server.URL}}}, "users": []any{}}
	writeConfig := func() {
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	t.Setenv("KUBECONFIG", configPath)
	badPreferences := filepath.Join(dir, "kuberc")
	if err := os.WriteFile(badPreferences, []byte("invalid preferences that must not be loaded"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBERC", badPreferences)
	t.Setenv("KUBECTL_KUBERC", "true")
	home := filepath.Join(dir, "no-state")
	t.Setenv("KMX_HOME", home)
	a := captureTestApp()
	got, err := a.Discover(t.Context(), DiscoveryOptions{})
	if err == nil || got.Complete || len(got.Rows) != 3 || requests.Load() != 0 {
		t.Fatal("projection", got, err, requests.Load())
	}
	if got.Rows[0].Context != name || got.Rows[0].Current == nil || !*got.Rows[0].Current {
		t.Fatal(got)
	}
	cfg["current-context"] = ""
	cfg["contexts"] = []any{}
	writeConfig()
	got, err = a.Discover(t.Context(), DiscoveryOptions{})
	if err != nil || !got.Complete || len(got.Rows) != 0 {
		t.Fatal("empty", got, err)
	}
	cfg["current-context"] = name
	cfg["contexts"] = contexts[:1]
	writeConfig()
	for _, s := range []int32{200, 401, 403, 404} {
		status.Store(s)
		got, err = a.Discover(t.Context(), DiscoveryOptions{Context: name})
		if s == 200 {
			if err != nil || !got.Complete || len(got.Rows) != 1 {
				t.Fatal(got, err)
			}
			continue
		}
		want := map[int32]string{401: "unauthorized", 403: "read_denied", 404: "read_unknown"}[s]
		if err == nil || got.Complete || len(got.Rows) != 0 || got.Checks[0].Reason != want {
			t.Fatal(s, got, err)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("state created", err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), server.URL) {
		t.Fatal("endpoint escaped")
	}
}
