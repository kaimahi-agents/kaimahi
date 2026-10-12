package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
	"oras.land/oras-go/v2/content/oci"
)

func TestWorkspacePushHandlerResolvesWorkspace(t *testing.T) {
	ctx := context.Background()
	_, root, prepared := gitWorkspaceRepository(t, false)
	nested := filepath.Join(root, "nested", "deep")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := t.TempDir()
	writeFile(t, filepath.Join(unrelated, "workspace.yaml"), "broken: [\n")
	relative, err := filepath.Rel(unrelated, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cwd, override, wantErr string
		notFound                     bool
	}{
		{"nested discovery", nested, "", "", false},
		{"relative exact override", unrelated, relative, "", false},
		{"absolute exact override", unrelated, root, "", false},
		{"explicit missing marker", nested, filepath.Dir(nested), "--workspace <directory>", true},
		{"explicit missing directory", nested, filepath.Join(root, "missing"), "--workspace <directory>", true},
		{"missing discovery", t.TempDir(), "", "--workspace <directory>", true},
		{"invalid nearest marker", unrelated, "", "invalid workspace descriptor", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			layout := filepath.Join(t.TempDir(), "layout")
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			result, err := a.PushSuiteWorkspace(ctx, tc.override, "team:v1", layout, false, false, SuitePushProvenance{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || errors.Is(err, workspace.ErrNotFound) != tc.notFound {
					t.Fatalf("handler refusal = %v; want %q, notFound=%t", err, tc.wantErr, tc.notFound)
				}
				entries, stateErr := os.ReadDir(state)
				if stateErr != nil || len(entries) != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
					t.Fatal("resolution refusal prepared state or emitted output", stateErr)
				}
				if _, err := os.Stat(layout); !os.IsNotExist(err) {
					t.Fatal("resolution refusal wrote destination")
				}
				return
			}
			if err != nil || result.Descriptor.Digest != prepared.Descriptor.Digest {
				t.Fatalf("handler selected wrong source: %s, %v", result.Descriptor.Digest, err)
			}
			want := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, prepared.Descriptor.Digest)
			if diagnostics.String() != want {
				t.Fatalf("handler identity = %q; want %q", diagnostics.String(), want)
			}
		})
	}
}

func TestWorkspacePushHandlerAbsoluteOverrideWithoutCwd(t *testing.T) {
	_, root, prepared := gitWorkspaceRepository(t, false)
	t.Setenv("KMX_HOME", t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Remove(cwd); err != nil {
		t.Skip("platform cannot remove the current directory", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("platform can still resolve the removed current directory")
	}
	a := &App{Out: io.Discard}
	result, err := a.PushSuiteWorkspace(context.Background(), root, "team:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{})
	if err != nil || result.Descriptor.Digest != prepared.Descriptor.Digest {
		t.Fatalf("absolute override depended on cwd: %s, %v", result.Descriptor.Digest, err)
	}
}

// Catch reporting the public graph/fingerprint rather than the published OCI manifest.
func TestWorkspacePublicationDiagnosticTracksCaseOnlyEdits(t *testing.T) {
	ctx := context.Background()
	_, root, before := gitWorkspaceRepository(t, false)
	t.Setenv("KMX_HOME", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	var firstDigest digest.Digest
	var firstGraph string
	for _, edit := range []bool{false, true} {
		if edit {
			writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
		}
		out.Reset()
		diagnostics.Reset()
		layout := filepath.Join(t.TempDir(), "layout")
		result, err := a.PushSuiteWorkspace(ctx, root, "team:v1", layout, false, false, SuitePushProvenance{})
		if err != nil {
			t.Fatal(err)
		}
		store, err := oci.New(layout)
		if err != nil {
			t.Fatal(err)
		}
		published, err := store.Resolve(ctx, "team:v1")
		if err != nil {
			t.Fatal(err)
		}
		if result.Descriptor.Digest != published.Digest {
			t.Fatal("result differs from published identity")
		}
		want := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, published.Digest)
		if diagnostics.String() != want {
			t.Fatalf("diagnostic = %q, want %q", diagnostics.String(), want)
		}
		wantOut := fmt.Sprintf("Pushed native-Orka AgentSuite greeter to %s (%s)\nRequired member images: []\n", layout, published.Digest)
		if out.String() != wantOut {
			t.Fatalf("result changed: %q, want %q", out.String(), wantOut)
		}
		prepared, err := workspace.Prepare(ctx, root, filepath.Join(t.TempDir(), "cache"))
		if err != nil {
			t.Fatal(err)
		}
		native, err := workspace.Load(ctx, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if !edit {
			firstDigest, firstGraph = published.Digest, native.GraphDigest
			if firstDigest != before.Descriptor.Digest {
				t.Fatal("first publication changed prepared identity")
			}
		} else if published.Digest == firstDigest || native.GraphDigest != firstGraph {
			t.Fatal("case-only edit did not change exact OCI identity while preserving public graph")
		}
	}
}

type workspaceDiagnosticWriter struct{}

func (workspaceDiagnosticWriter) Write([]byte) (int, error) {
	return 0, errors.New("workspace diagnostic unavailable")
}

func TestWorkspaceDiagnosticFailureRefusesPublication(t *testing.T) {
	_, root, _ := gitWorkspaceRepository(t, false)
	t.Setenv("KMX_HOME", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	ref := strings.TrimPrefix(server.URL, "http://") + "/suite:v1"
	for _, layout := range []string{filepath.Join(t.TempDir(), "layout"), ""} {
		var out bytes.Buffer
		a := &App{Out: &out, Err: workspaceDiagnosticWriter{}}
		_, err := a.PushSuiteWorkspace(context.Background(), root, ref, layout, true, false, SuitePushProvenance{})
		if err == nil || !strings.Contains(err.Error(), "workspace diagnostic unavailable") || requests.Load() != 0 || out.Len() != 0 {
			t.Fatalf("diagnostic failure did not refuse before publication: %v, requests %d", err, requests.Load())
		}
		if layout != "" {
			if _, err := os.Stat(layout); !os.IsNotExist(err) {
				t.Fatal("diagnostic failure wrote destination")
			}
		}
	}
}

func TestWorkspaceDiagnosticQuotesAbsolutePath(t *testing.T) {
	_, root, _ := gitWorkspaceRepository(t, false)
	unsafe := filepath.Join(t.TempDir(), "workspace\n\x1b[31m\u202e")
	if err := os.Rename(root, unsafe); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(unsafe))
	t.Setenv("KMX_HOME", t.TempDir())
	var diagnostics bytes.Buffer
	a := &App{Out: io.Discard, Err: &diagnostics}
	result, err := a.PushSuiteWorkspace(context.Background(), filepath.Base(unsafe), "team:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", unsafe, result.Descriptor.Digest)
	if diagnostics.String() != want || strings.Count(diagnostics.String(), "\n") != 1 {
		t.Fatalf("unsafe/multiline diagnostic: %q", diagnostics.String())
	}
}

func TestWorkspacePublicationIdentityContract(t *testing.T) {
	ctx := context.Background()
	_, root, before := gitWorkspaceRepository(t, false)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/suite:v1"
	a := &App{Out: io.Discard}
	for _, tc := range []struct {
		name               string
		expected           digest.Digest
		wantErr, noPrepare bool
	}{
		{"matching", before.Descriptor.Digest, false, false},
		{"empty", "", true, true},
		{"malformed", "sha256:bad", true, true},
		{"wrong algorithm", digest.Digest("sha512:" + strings.Repeat("0", 128)), true, true},
		{"case-only", before.Descriptor.Digest, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			if tc.name == "case-only" {
				writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
			}
			destination := filepath.Join(t.TempDir(), "destination")
			layouts := []string{destination}
			if tc.wantErr {
				layouts = append(layouts, "")
			}
			for _, layout := range layouts {
				result, err := a.pushSuiteWorkspace(ctx, root, reference, layout, true, false, SuitePushProvenance{Enabled: true, Require: true}, &tc.expected)
				if (err != nil) != tc.wantErr || requests.Load() != 0 || (!tc.wantErr && result.Descriptor.Digest != before.Descriptor.Digest) {
					t.Fatal("unexpected publication identity/outcome or registry side effect", err)
				}
			}
			if _, err := os.Stat(destination); tc.wantErr && !os.IsNotExist(err) {
				t.Fatal("refusal wrote destination")
			}
			if _, err := os.Stat(filepath.Join(state, "suites")); tc.noPrepare != os.IsNotExist(err) {
				t.Fatal("preparation side effect differs from contract")
			}
		})
	}
}
