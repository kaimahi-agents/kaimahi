package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestWorkspaceBarePushReadsSavedRepositoryAndTransportOnce(t *testing.T) {
	for _, mode := range []string{"saved", "explicit false", "missing proof"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			root, _ := suiteEvalSource(t)
			prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			tag := "s-" + prepared.Descriptor.Digest.Encoded()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "HEAD" || r.URL.Path != "/v2/team/agents/manifests/"+tag {
					t.Errorf("wrong bare destination: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				// Operational config can change after resolution without redirecting
				// later publication/output; it is not part of prepared S.
				if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("invalid: [\n"), 0600); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", prepared.Descriptor.MediaType)
				w.Header().Set("Content-Length", strconv.FormatInt(prepared.Descriptor.Size, 10))
				w.Header().Set("Docker-Content-Digest", prepared.Descriptor.Digest.String())
			}))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			repository := host + "/team/agents"
			cfg := fmt.Sprintf("version: 1\npublishRepository: %s\nregistries:\n  '%s': {plainHTTP: true}\n", repository, host)
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"push", "--workspace", root, "--provenance=false"}
			if mode != "missing proof" {
				args = append(args, "--unevaluated")
			}
			if mode == "explicit false" {
				args = append(args, "--plain-http=false")
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err = execute(args, deps)
			if mode == "saved" {
				if err != nil || requests.Load() != 1 || !strings.Contains(out.String(), repository+":"+tag) || !strings.Contains(out.String(), repository+"@"+prepared.Descriptor.Digest.String()) {
					t.Fatalf("saved bare push failed: %v requests=%d stdout=%s", err, requests.Load(), out.String())
				}
			} else if err == nil || requests.Load() != 0 {
				t.Fatalf("refusal/downgrade failed: %v requests=%d", err, requests.Load())
			}
			if *loads != 0 {
				t.Fatal("bare push loaded cluster settings")
			}
		})
	}
}

func TestWorkspacePushEvaluatedDefaultAndBareValidation(t *testing.T) {
	for _, mode := range []string{"default", "false opt-out", "unevaluated", "conflict", "empty evaluation", "bare missing repository", "bare layout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			layout := filepath.Join(t.TempDir(), "layout")
			args := []string{"push", "--workspace", root, "--to-layout", layout, "agents:slide-v1"}
			want := "no completed passing evidence"
			switch mode {
			case "false opt-out":
				args = append(args, "--unevaluated=false")
			case "unevaluated":
				args = append(args, "--unevaluated")
				want = ""
			case "conflict":
				args = append(args, "--unevaluated", "--evaluation", "private.json")
				want = "cannot"
			case "empty evaluation":
				args = append(args, "--evaluation=")
				want = "non-empty --evaluation"
			case "bare missing repository":
				args = []string{"push", "--workspace", root}
				want = "publishRepository"
			case "bare layout":
				args = args[:len(args)-1]
				want = "reference"
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(args, deps)
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if *loads != 0 {
				t.Fatal("push loaded cluster configuration")
			}
			_, statErr := os.Stat(layout)
			if want != "" && !os.IsNotExist(statErr) {
				t.Fatal("refusal transferred")
			}
			if want == "" && statErr != nil {
				t.Fatal("explicit unevaluated push failed", statErr)
			}
		})
	}
}
