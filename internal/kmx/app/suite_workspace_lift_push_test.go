package app

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedPublicationCompletionRequiresSuccessfulFinalization(t *testing.T) {
	for _, mode := range []string{"output failure", "corrupt completion"} {
		t.Run(mode, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			commitStoreReceipt(t, store, suite, raw)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "HEAD" {
					t.Errorf("unexpected registry effect: %s", r.Method)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Docker-Content-Digest", suite.Digest)
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Content-Length", "123")
			}))
			defer server.Close()
			opt := SuiteWorkspacePushOptions{Workspace: store.workspace, Repository: strings.TrimPrefix(server.URL, "http://") + "/agents", PlainHTTP: true}
			a := &App{Out: io.Discard, Err: io.Discard}
			cause := errors.New("output unavailable")
			if mode == "output failure" {
				a.Out = suiteSessionsCauseWriter{cause: cause}
			}
			result, err := a.PushSuiteWorkspaceManaged(t.Context(), opt)
			files, globErr := filepath.Glob(filepath.Join(store.partition, "lift-publication-*.json"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if mode == "output failure" {
				if !errors.Is(err, cause) || result.Descriptor.Digest.String() != suite.Digest || len(files) != 0 {
					t.Fatalf("unfinalized publication registered: result=%+v err=%v records=%v", result, err, files)
				}
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("completed publication missing: err=%v records=%v", err, files)
			}
			if err := os.WriteFile(files[0], []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			before := requests.Load()
			if _, err := a.PushSuiteWorkspaceManaged(t.Context(), opt); !errors.Is(err, errWorkspaceLiftState) {
				t.Fatalf("unsafe completion accepted: %v", err)
			}
			if requests.Load() != before {
				t.Fatal("unsafe completion reached registry")
			}
		})
	}
}

// Without standalone publication registration, composing the individually
// available eval/push verbs re-enters publication instead of skipping it.
func TestWorkspaceLiftSkipsCompletedStandaloneEvalAndPush(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	a.Out, a.Err = io.Discard, io.Discard
	evalOpt := opt.Evaluation
	evalOpt.ManagedPass = true
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), evalOpt)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if len(state.creates) != 4 {
		t.Fatal("standalone evaluation did not execute its full candidate inventory")
	}
	_, err = a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: opt.Workspace, Repository: opt.Repository, PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	lift, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer lift.Close()
	result, err := a.LiftWorkspace(t.Context(), lift)
	if err != nil {
		t.Fatal(err)
	}
	if result.Eval.Status != "skipped" || result.Push.Status != "skipped" || result.Lift.Status != "complete" {
		t.Fatalf("already completed steps repeated: %+v", result)
	}
	if len(state.creates) != 4 || *writes != 0 {
		t.Fatal("composing completed verbs executed or republished")
	}
}
