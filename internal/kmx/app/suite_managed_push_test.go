package app

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"oras.land/oras-go/v2/content/oci"
)

func TestSuiteManagedPushProofModesAndExactGraph(t *testing.T) {
	for _, mode := range []string{"automatic", "missing", "edited", "equivalent spelling", "explicit", "invalid explicit", "unevaluated", "explicit overrides corrupt store", "unevaluated ignores corrupt store"} {
		t.Run(mode, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			root := store.workspace
			record := ""
			if mode != "missing" {
				record = commitStoreReceipt(t, store, suite, raw)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			receiptFile := filepath.Join(privateSuiteDir(t), "passing.json")
			if err := os.WriteFile(receiptFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			opt := SuiteWorkspacePushOptions{Workspace: root, Reference: "suite:slide-tag", Layout: filepath.Join(t.TempDir(), "layout")}
			switch mode {
			case "edited":
				writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: changed\ninput: four\nexpectContains: ['4']\n")
			case "equivalent spelling":
				member := filepath.Join(root, "agents/greeter/member.yaml")
				data, err := os.ReadFile(member)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, member, string(data)+"# spelling only\n")
			case "explicit":
				opt.Evaluation = receiptFile
			case "invalid explicit":
				opt.Evaluation = filepath.Join(privateSuiteDir(t), "missing.json")
			case "unevaluated":
				opt.Unevaluated = true
			case "explicit overrides corrupt store":
				opt.Evaluation = receiptFile
				if err := os.WriteFile(record, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unevaluated ignores corrupt store":
				opt.Unevaluated = true
				if err := os.WriteFile(record, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var diagnostics bytes.Buffer
			result, err := (&App{Out: io.Discard, Err: &diagnostics}).PushSuiteWorkspaceManaged(t.Context(), opt)
			refuse := mode == "missing" || mode == "edited" || mode == "invalid explicit"
			if (err != nil) != refuse {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if refuse {
				if _, err := os.Stat(opt.Layout); !os.IsNotExist(err) {
					t.Fatal("refusal transferred")
				}
				return
			}
			if result.Descriptor.Digest.String() != suite.Digest {
				t.Fatal("wrong exact S")
			}
			target, err := oci.New(opt.Layout)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := target.Resolve(t.Context(), "suite:slide-tag")
			if err != nil || descriptor.Digest.String() != suite.Digest {
				t.Fatalf("explicit tag replaced: %v", err)
			}
			if mode == "automatic" && !strings.Contains(diagnostics.String(), record) {
				t.Fatal("selected proof not disclosed")
			}
		})
	}
}

func TestSuiteManagedPushBareTagAndExplicitTag(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(strconv.FormatBool(bare), func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			commitStoreReceipt(t, store, suite, raw)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			tag := "slide-v1"
			if bare {
				tag = "s-" + strings.TrimPrefix(suite.Digest, "sha256:")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "HEAD" || r.URL.Path != "/v2/agents/manifests/"+tag {
					t.Errorf("wrong explicit/derived reference: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				// Existing exact digest exercises the normal registry no-op tag policy.
				w.Header().Set("Docker-Content-Digest", suite.Digest)
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Content-Length", "123")
			}))
			defer server.Close()
			repository := strings.TrimPrefix(server.URL, "http://") + "/agents"
			opt := SuiteWorkspacePushOptions{Workspace: store.workspace, Repository: repository, PlainHTTP: true}
			if !bare {
				opt.Reference = repository + ":slide-v1"
			}
			var out bytes.Buffer
			result, err := (&App{Out: &out, Err: io.Discard}).PushSuiteWorkspaceManaged(t.Context(), opt)
			if err != nil || result.Reference != repository+":"+tag || requests.Load() != 1 {
				t.Fatalf("tag/digest changed: %+v %v requests=%d", result, err, requests.Load())
			}
			if !strings.Contains(out.String(), repository+"@"+suite.Digest) {
				t.Fatal("full pinned digest missing")
			}
		})
	}
}

func TestSuiteManagedPushNoNetworkWithoutProofAndInvalidOverride(t *testing.T) {
	for _, mode := range []string{"missing", "invalid explicit", "conflict", "missing repository", "bare layout"} {
		t.Run(mode, func(t *testing.T) {
			store, _, _ := passStoreFixture(t)
			store.Close()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			opt := SuiteWorkspacePushOptions{Workspace: store.workspace, Reference: strings.TrimPrefix(server.URL, "http://") + "/agents:slide", PlainHTTP: true, Provenance: SuitePushProvenance{Enabled: true}}
			switch mode {
			case "invalid explicit":
				opt.Evaluation = "missing.json"
			case "conflict":
				opt.Evaluation = "missing.json"
				opt.Unevaluated = true
			case "missing repository":
				opt.Reference = ""
			case "bare layout":
				opt.Reference = ""
				opt.Repository = "registry.example/agents"
				opt.Layout = filepath.Join(t.TempDir(), "layout")
			}
			if _, err := (&App{Out: io.Discard}).PushSuiteWorkspaceManaged(t.Context(), opt); err == nil || requests.Load() != 0 {
				t.Fatalf("refusal had network effects: %v requests=%d", err, requests.Load())
			}
		})
	}
}
