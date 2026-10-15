package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// External registry boundary backed by the actual prepared graph; publication
// starts with an absent tag, and copied blobs and the tag are retained separately.
func workspaceLiftFixture(t *testing.T, configure ...func(string)) (*App, WorkspaceLiftOptions, *publicCandidateFixture, *nativeReadFixture, *evalFixture, *int) {
	t.Helper()
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	a, reads, f, state := publicNativeEvaluationFixture(t)
	eval := nativeEvaluationOptions(t)
	eval.Out = ""
	eval.ResultPort = f.opt.ResultPort
	for _, edit := range configure {
		edit(eval.Workspace)
	}
	prepared, err := workspace.Prepare(t.Context(), eval.Workspace, filepath.Join(t.TempDir(), "registry-source"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{nativeCanary + " first", nativeCanary + " second"} {
		f.answers[c] = nativeCanary + " answer"
	}
	reads.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = suite.Model
	writes := new(int)
	blobs := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		part := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if r.Method != "GET" && r.Method != "HEAD" {
			*writes++
			t.Errorf("unexpected registry write %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if strings.HasPrefix(part, "s-") {
			part = suite.Digest
		}
		raw, ok := blobs[part]
		if !ok {
			raw, err = os.ReadFile(filepath.Join(prepared.Path, "blobs", "sha256", strings.TrimPrefix(part, "sha256:")))
			ok = err == nil
			if ok {
				blobs[part] = raw
			}
		}
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Docker-Content-Digest", part)
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		if r.Method == "GET" {
			w.Write(raw)
		}
	}))
	t.Cleanup(server.Close)
	opt := WorkspaceLiftOptions{Workspace: eval.Workspace, Evaluation: eval, Destination: ReleaseLiftOptions{ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", PlainHTTP: true}, EvalName: "dev", DestinationName: "prod", Repository: strings.TrimPrefix(server.URL, "http://") + "/agents"}
	a.Cfg.Confirm = "kind-test"
	old := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		// The canonical destination uses the existing create-only release fixture.
		if slices.Contains(args, "-f") {
			raw, _ := io.ReadAll(cmd.Stdin)
			cmd.Stdin = bytes.NewReader(raw)
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			if doc["kind"] == "Provider" || doc["kind"] == "Agent" {
				if orkaObjectName(doc) == "greeter" {
					return reconcileCaptureFixture(ctx, cmd)
				}
			}
		}
		if i := slices.Index(args, "get"); i >= 0 && i+2 < len(args) {
			if (args[i+1] == "providers.core.orka.ai" || args[i+1] == "agents.core.orka.ai") && args[i+2] == "greeter" {
				return reconcileCaptureFixture(ctx, cmd)
			}
		}
		return old(ctx, cmd)
	}
	return a, opt, state, reads, f, writes
}

func TestWorkspaceLiftColdAndVerifiedRepeat(t *testing.T) {
	a, opt, state, _, _, writes := workspaceLiftFixture(t)
	var out, diagnostics bytes.Buffer
	a.Out = &out
	a.Err = &diagnostics
	for i := 0; i < 2; i++ {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.LiftWorkspace(t.Context(), p)
		if err != nil {
			t.Fatalf("run %d: %+v %v", i, result, err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		want := "complete"
		if i == 1 {
			want = "skipped"
		}
		if result.Eval.Status != want || result.Push.Status != want || result.Lift.Status != want || result.Eval.Passed != 2 || result.Eval.Total != 2 {
			t.Fatalf("run %d: %+v", i, result)
		}
	}
	if len(state.creates) != 4 || *writes != 0 {
		t.Fatal("repeat executed Tasks or republished", state.creates, *writes)
	}
	if len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 2 {
		t.Fatal("repeat applied")
	}
	if strings.Count(strings.TrimSpace(diagnostics.String()), "\n") != 7 || out.Len() != 0 {
		t.Fatal("not four stderr lines per invocation", diagnostics.String(), out.String())
	}
}

func TestWorkspaceLiftPlanColdOffline(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	eval := nativeEvaluationOptions(t)
	eval.Out = ""
	opt := WorkspaceLiftOptions{Workspace: eval.Workspace, Evaluation: eval, Destination: ReleaseLiftOptions{ToContext: "remote-prod", Inference: "provider:inference"}, EvalName: "dev", DestinationName: "prod", Repository: "registry.invalid/agents", PlanOnly: true}
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var stderr bytes.Buffer
	result, err := (&App{Err: &stderr, Out: io.Discard}).LiftWorkspace(t.Context(), p)
	if err != nil || result.Eval.Status != "not-attempted" || !strings.Contains(result.Plan.Disclosure, "retention") {
		t.Fatal(result, err)
	}
	state, _ := config.StateDir()
	if _, err := os.Stat(filepath.Join(state, "passing")); !os.IsNotExist(err) {
		t.Fatal("plan wrote operational state", err)
	}
}

func TestWorkspaceLiftFailureStopsLaterEffects(t *testing.T) {
	for _, mode := range []string{"case", "cleanup", "consent", "source", "strict", "drift"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, reads, f, writes := workspaceLiftFixture(t)
			switch mode {
			case "case":
				f.answers[nativeCanary+" first"] = "wrong"
			case "cleanup":
				state.beforeDelete = func(context.Context, *exec.Cmd) error { return errors.New("denied") }
			case "consent":
				a.Cfg.Confirm = "different"
			case "strict":
				no := false
				opt.Promote = &no
			}
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if mode == "source" {
				writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: changed\ninput: changed\nexpectContains: [yes]\n")
			}
			if mode == "drift" {
				n := 0
				reads.change = func(key string, _ int, obj map[string]any) error {
					if key == "providers.core.orka.ai/inference" {
						n++
						if n > 3 {
							nativeMetadata(obj)["resourceVersion"] = "2"
						}
					}
					return nil
				}
			}
			r, err := a.LiftWorkspace(t.Context(), p)
			if err == nil || r.Push.Status == "complete" || *writes != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
				t.Fatal("failed pipeline advanced", r, err, *writes)
			}
			if mode == "case" && (r.Eval.Passed != 0 || r.Eval.Total != 2 || !strings.Contains(r.Eval.Detail, "failed")) {
				t.Fatal("missing typed failure", r)
			}
		})
	}
}
