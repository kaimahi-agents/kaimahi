package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
)

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
