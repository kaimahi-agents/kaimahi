package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

func runWorkspaceLift(t *testing.T, a *App, opt WorkspaceLiftOptions) WorkspaceLiftResult {
	t.Helper()
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.LiftWorkspace(t.Context(), p)
	closeErr := p.Close()
	if err != nil || closeErr != nil {
		t.Fatal(r, err, closeErr)
	}
	return r
}

func TestWorkspaceLiftCachedPassAtCapacityWithoutJudge(t *testing.T) {
	a, opt, state, _, f, _ := workspaceLiftFixture(t)
	// Register real judged candidate proof, then remove all judge configuration.
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), suiteJudgeYAML)
	f.answers[suiteJudgeCanary] = "open-ended answer"
	endpoint, calls := suiteJudgeFixtureServer(t, func(*http.Request, int) int { return 90 })
	opt.Evaluation.JudgeBaseURL = endpoint
	opt.PlanOnly = true
	cold, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	cold.Close()
	if !cold.plan.Judging || !strings.Contains(cold.plan.Disclosure, "answers and rubrics") {
		t.Fatal("judge transfer omitted")
	}
	evalOpt := opt.Evaluation
	evalOpt.ManagedPass = true
	ep, err := PrepareSuiteWorkspaceEvaluation(t.Context(), evalOpt)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), ep); err != nil {
		t.Fatal(err)
	}
	store := ep.store
	receipt, _, err := store.latest(t.Context(), ep.suite)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, ep.suite)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= maxSuitePassRecords; i++ {
		record := suitePassRecord{1, store.identity, store.suiteDigest, uint64(i), raw}
		encoded, err := spec.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.partition, fmt.Sprintf("%020d.json", i)), encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ep.Close(); err != nil {
		t.Fatal(err)
	}
	opt.PlanOnly = false
	opt.Evaluation.JudgeBaseURL = ""
	// Cached eval skips even with an unreachable registry: refusal happens at
	// push, after a typed skip, rather than demanding judge setup/reservation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	opt.Repository = strings.TrimPrefix(server.URL, "http://") + "/agents"
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || r.Eval.Status != "skipped" || r.Eval.Passed != 2 || calls.Load() != 2 || len(state.creates) != 4 {
		t.Fatal("cached full partition demanded fresh judge/eval", r, err, calls.Load(), state.creates)
	}
}

func TestWorkspaceLiftRefusesUnsafeCompletionAndLiveDrift(t *testing.T) {
	for _, mode := range []string{"corrupt", "unknown", "mode", "link", "scope", "missing resource", "uid", "spec", "ready", "inference", "runtime", "receipt", "source"} {
		t.Run(mode, func(t *testing.T) {
			a, opt, state, reads, _, writes := workspaceLiftFixture(t)
			runWorkspaceLift(t, a, opt)
			before := len(state.creates)
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			files, err := filepath.Glob(filepath.Join(p.store.partition, "lift-placement-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal(files, err)
			}
			file := files[0]
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "corrupt":
				err = os.WriteFile(file, []byte("{"), 0600)
			case "unknown":
				err = os.WriteFile(file, bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1), 0600)
			case "mode":
				err = os.Chmod(file, 0644)
			case "link":
				err = os.Rename(file, file+".original")
				if err == nil {
					err = os.Symlink(file+".original", file)
				}
			case "scope":
				err = os.WriteFile(file, bytes.Replace(raw, []byte(`"scope":"placement`), []byte(`"scope":"other`), 1), 0600)
			case "inference":
				nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "99"
			case "runtime":
				nativeMetadata(reads.objects["deploy/-o"]["items"].([]any)[0].(map[string]any))["uid"] = "replacement"
			case "receipt":
				proofs, _ := filepath.Glob(filepath.Join(p.store.partition, "[0-9]*.json"))
				err = os.WriteFile(proofs[0], []byte("corrupt"), 0600)
			case "source":
				writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: new\ninput: new\nexpectContains: [new]\n")
			default:
				path := filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), "agents.core.orka.ai.json")
				data, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				var doc map[string]any
				if json.Unmarshal(data, &doc) != nil {
					t.Fatal("bad fixture")
				}
				switch mode {
				case "missing resource":
					err = os.Remove(path)
				case "uid":
					nativeMetadata(doc)["uid"] = "replacement"
				case "spec":
					doc["spec"].(map[string]any)["model"] = "replacement"
				case "ready":
					doc["status"] = map[string]any{"ready": false}
					old := a.captureCommand
					a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
						if i := slices.Index(cmd.Args, "get"); i >= 0 && i+2 < len(cmd.Args) && cmd.Args[i+1] == "agents.core.orka.ai" && cmd.Args[i+2] == "greeter" {
							return json.NewEncoder(cmd.Stdout).Encode(doc)
						}
						return old(ctx, cmd)
					}
				}
				if mode != "missing resource" {
					err = os.WriteFile(path, mustJSON(t, doc), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := a.LiftWorkspace(t.Context(), p)
			if err == nil || r.Lift.Status == "skipped" || len(state.creates) != before || *writes != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 2 {
				t.Fatal("drift/unsafe record authorized effects", r, err)
			}
		})
	}
}

func TestWorkspaceLiftLatePublicationUnknownAndNoDestinationEffects(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	var copied atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "HEAD":
			w.WriteHeader(404)
		case "POST":
			w.Header().Set("Location", "/v2/agents/blobs/uploads/upload")
			w.WriteHeader(202)
		case "PUT":
			io.Copy(io.Discard, r.Body)
			if strings.Contains(r.URL.Path, "/blobs/uploads/") {
				copied.Add(1)
				w.Header().Set("Docker-Content-Digest", r.URL.Query().Get("digest"))
				w.WriteHeader(201)
			} else {
				w.WriteHeader(403)
			}
		default:
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	opt.Repository = strings.TrimPrefix(server.URL, "http://") + "/agents"
	opt.JSON = true
	var stdout, stderr bytes.Buffer
	a.Out = &stdout
	a.Err = &stderr
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || copied.Load() == 0 || r.Push.Status != "unknown" || strings.Contains(r.Push.Detail, "nothing published") || r.Lift.Status != "not-attempted" || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
		t.Fatal("late partial registry transfer misreported", copied.Load(), r, err)
	}
	var decoded WorkspaceLiftResult
	decoder := json.NewDecoder(&stdout)
	if decoder.Decode(&decoded) != nil || decoder.Decode(&decoded) != io.EOF || decoded.Push.Status != "unknown" || stderr.Len() != 0 {
		t.Fatal("not one JSON result", stdout.String(), stderr.String())
	}
}

func TestWorkspaceLiftCompletionFailurePreservesSuccessfulEffects(t *testing.T) {
	for _, stage := range []string{"publication", "placement"} {
		t.Run(stage, func(t *testing.T) {
			a, opt, _, _, _, _ := workspaceLiftFixture(t)
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			closed := false
			a.Out = io.Discard
			// The source check after eval is a reliable boundary before publication;
			// turning the completion target into an occupied file makes exclusive link
			// fail, without preventing actual registry/placement effects.
			a.Err = io.Discard
			pattern := filepath.Join(p.store.partition, "lift-"+stage+"-*.json")
			oldCapture := a.captureCommand
			configReads := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "config") {
					configReads++
				}
				if !closed && configReads == 2 && slices.Contains(cmd.Args, "config") {
					// Completion handles already preflighted before the combined consent.
					names, _ := filepath.Glob(pattern)
					if len(names) == 0 { // absent output is deliberate; derive frozen name.
						scope := opt.Repository
						if stage == "placement" {
							scope += "\n" + opt.Destination.ToContext + "\n" + opt.Destination.ToNamespace + "\n" + opt.Destination.Inference
						}
						path := filepath.Join(p.store.partition, fmt.Sprintf("lift-%s-%x.json", stage, sha256.Sum256([]byte(scope))))
						if err := os.WriteFile(path, []byte("occupied"), 0600); err != nil {
							t.Fatal(err)
						}
						closed = true
					}
				}
				return oldCapture(ctx, cmd)
			}
			r, err := a.LiftWorkspace(t.Context(), p)
			if err == nil || !closed || r.Push.Status != "complete" {
				t.Fatal("effect lost after record failure", r, err)
			}
			if stage == "publication" && (r.Lift.Status != "not-attempted" || !strings.Contains(r.Push.Detail, "publication succeeded")) {
				t.Fatal(r)
			}
			if stage == "placement" && (r.Lift.Status != "complete" || r.Placement == nil || r.Placement.State != "ready" || !strings.Contains(r.Lift.Detail, "placement succeeded")) {
				t.Fatal(r)
			}
		})
	}
}

func TestWorkspaceLiftChangedSuiteDoesNotConsumePreviousPass(t *testing.T) {
	a, opt, _, _, f, _ := workspaceLiftFixture(t)
	first := runWorkspaceLift(t, a, opt)
	// Clear destination to isolate exact-S proof selection from occupied-target
	// refusal. This is a test fixture, never product cleanup/recovery.
	for _, kind := range []string{"providers", "agents"} {
		if err := os.Remove(filepath.Join(os.Getenv("KMX_RECONCILE_DIR"), kind+".core.orka.ai.json")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: changed\ninput: changed\nexpectContains: [new]\n")
	f.answers["changed"] = "wrong"
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || r.Plan.SuiteDigest == first.Plan.SuiteDigest || r.Eval.Status != "failed" || r.Push.Status != "not-attempted" {
		t.Fatal("old S pass reused", r, err)
	}
}
