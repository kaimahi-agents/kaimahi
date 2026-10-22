package app

import (
	"encoding/json"
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

	suitecore "github.com/agent-primitives/suite/core"
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

// Root registry push registers a publication completion only after it selects
// an AgentSessions passing receipt v2, which records its binding digest. A v1
// receipt refuses before any registry request or completion record, both from
// managed lookup and from --evaluation.
func TestManagedPublicationAgentSessionsRequiresReceiptV2(t *testing.T) {
	p, subject, observations := sessionsPassingFixture(t)
	receipt, err := newSuiteSessionsPassingReceipt(t.Context(), p, subject, observations)
	if err != nil {
		t.Fatal(err)
	}
	current, err := canonicalSuitePassingReceipt(t.Context(), receipt, p.suite)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(current, &document); err != nil {
		t.Fatal(err)
	}
	document["domain"] = suiteSessionsPassingDomainV1
	delete(document["agentsessions"].(map[string]any)["subject"].(map[string]any), "bindingDigest")
	old, err := suitecore.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	cache := filepath.Join(os.Getenv("KMX_HOME"), "suites")
	store, err := openSuitePassStore(t.Context(), p.root, cache, p.suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	partition := store.partition
	b, err := suitecore.Marshal(suitePassRecord{1, store.identity, p.suite.Digest, 1, old})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partition, fmt.Sprintf("%020d.json", 1)), b, 0600); err != nil {
		t.Fatal(err)
	}
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
		w.Header().Set("Docker-Content-Digest", p.suite.Digest)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Content-Length", "123")
	}))
	defer server.Close()
	records := func() []string {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(partition, "lift-publication-*.json"))
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	evidence := privateSuiteDir(t)
	write := func(name string, raw []byte) string {
		t.Helper()
		path := filepath.Join(evidence, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := &App{Out: io.Discard, Err: io.Discard}
	opt := SuiteWorkspacePushOptions{Workspace: p.root, Repository: strings.TrimPrefix(server.URL, "http://") + "/agents", PlainHTTP: true}

	if _, err := a.PushSuiteWorkspaceManaged(t.Context(), opt); !errors.Is(err, errSuitePassSessionsV1) {
		t.Fatalf("managed receipt v1: %v", err)
	}
	explicitOld := opt
	explicitOld.Evaluation = write("old.json", old)
	if _, err := a.PushSuiteWorkspaceManaged(t.Context(), explicitOld); !errors.Is(err, errSuiteSessionsReceiptV1) {
		t.Fatalf("--evaluation receipt v1: %v", err)
	}
	if requests.Load() != 0 || len(records()) != 0 {
		t.Fatalf("refused proof reached registry or registered publication: requests=%d records=%v", requests.Load(), records())
	}

	explicit := opt
	explicit.Evaluation = write("current.json", current)
	result, err := a.PushSuiteWorkspaceManaged(t.Context(), explicit)
	if err != nil || result.Descriptor.Digest.String() != p.suite.Digest || len(records()) != 1 {
		t.Fatalf("--evaluation with bindingDigest: result=%+v err=%v records=%v", result, err, records())
	}
	store, err = openSuitePassStore(t.Context(), p.root, cache, p.suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	commitStoreReceipt(t, store, p.suite, current)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PushSuiteWorkspaceManaged(t.Context(), opt); err != nil || len(records()) != 1 {
		t.Fatalf("managed proof with bindingDigest: err=%v records=%v", err, records())
	}
}
