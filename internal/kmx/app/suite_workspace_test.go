package app

import (
	"bytes"
	"context"
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
)

// The handler owns cwd resolution and reports the full artifact, not the graph.
func TestWorkspacePushHandlerDiscoveryAndPublicationIdentity(t *testing.T) {
	ctx := context.Background()
	_, root, before := gitWorkspaceRepository(t, false)
	unsafe := filepath.Join(t.TempDir(), "workspace\n\x1b[31m\u202e")
	if err := os.Rename(root, unsafe); err != nil {
		t.Fatal(err)
	}
	root = unsafe
	t.Chdir(root)
	t.Setenv("KMX_HOME", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	var firstGraph string
	for _, edit := range []bool{false, true} {
		if edit {
			writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
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
			firstGraph = native.GraphDigest
		} else if prepared.Descriptor.Digest == before.Descriptor.Digest || native.GraphDigest != firstGraph {
			t.Fatal("case-only edit must change OCI identity while preserving the public graph")
		}
		out.Reset()
		diagnostics.Reset()
		layout := filepath.Join(t.TempDir(), "layout")
		result, err := a.PushSuiteWorkspace(ctx, "", "team:v1", layout, false, false, SuitePushProvenance{})
		if err != nil || result.Descriptor.Digest != prepared.Descriptor.Digest {
			t.Fatalf("handler selected wrong artifact: %s, %v", result.Descriptor.Digest, err)
		}
		want := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, prepared.Descriptor.Digest)
		if diagnostics.String() != want || strings.Count(diagnostics.String(), "\n") != 1 {
			t.Fatalf("handler diagnostic = %q, want safely quoted %q", diagnostics.String(), want)
		}
	}
	t.Run("diagnostic failure precedes publication", func(t *testing.T) {
		layout := filepath.Join(t.TempDir(), "layout")
		out.Reset()
		a.Err = evaluationDiffFailWriter{}
		_, err := a.PushSuiteWorkspace(ctx, "", "team:v1", layout, false, false, SuitePushProvenance{})
		if err == nil || !strings.Contains(err.Error(), "PRIVATE_WRITER_CANARY") || out.Len() != 0 {
			t.Fatalf("diagnostic failure did not refuse publication: %v", err)
		}
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatal("diagnostic failure wrote destination")
		}
	})
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
