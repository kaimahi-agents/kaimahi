package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Overlay Task admission only; native reads/placement and result transport retain
// their existing fixtures, including real private state and pinned HTTP socket.
func nativeEvaluationFixture(t *testing.T) (*App, *suiteNativePlacement, suitePassingInventoryCase, *evalFixture) {
	t.Helper()
	a, reads, p, _ := nativePlacementFixture(t)
	placement, err := a.placeSuiteNative(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	f := newEvalFixture(t, nil)
	f.nativeReads = reads
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if slices.Contains(args, "token") || slices.Contains(args, "services") || slices.Contains(args, "service") || slices.Contains(args, orkaResultAccount) || slices.Contains(args, "tasks.core.orka.ai") {
			return cmd.Run()
		}
		if slices.Contains(args, "-f") {
			raw, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				return err
			}
			cmd.Stdin = bytes.NewReader(raw)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return err
			}
			if doc["kind"] == "Task" {
				if slices.Contains(args, "--dry-run=server") {
					return json.NewEncoder(cmd.Stdout).Encode(doc)
				}
				return cmd.Run()
			}
		}
		return capture(ctx, cmd)
	}
	a.Out, a.Err = f.out, &bytes.Buffer{}
	raw := []byte("id: native\ninput: '  " + nativeCanary + " prompt  '\nassertions:\n- {id: exact, type: contains, value: '" + nativeCanary + "'}\n")
	c, err := agentruntime.ParseEvaluationCase(raw)
	if err != nil {
		t.Fatal(err)
	}
	frame := suitePassingInventoryCase{Case: c, Metadata: suitePassingCase{ID: c.ID, CaseDigest: agentruntime.EvaluationCaseDigest(raw), InputDigest: agentruntime.EvaluationInputDigest(c.Input)}}
	f.answers[c.Input] = nativeCanary + " answer"
	return a, placement, frame, f
}

func nativeEvaluationOptions(t *testing.T) SuiteWorkspaceEvaluationOptions {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer privately")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "packaged-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: '"+nativeCanary+" first'\nexpectContains: ['"+nativeCanary+"']\n")
	writeFile(t, filepath.Join(root, "eval/second.yaml"), "id: second\ninput: '"+nativeCanary+" second'\nexpectContains: ['"+nativeCanary+"']\n")
	return SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", Out: filepath.Join(privateSuiteDir(t), "passing.json"), CaseTimeout: 30 * time.Second, ResultPort: "19180"}
}

func TestSuiteWorkspaceEvaluationOfflineRefusals(t *testing.T) {
	// An unsupported evaluator must refuse before output anchoring/configuration.
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	opt.Out = filepath.Join(opt.Workspace, "forbidden.json")
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: first\ninput: private\nassertions: [{id: tool, type: toolCalled, tool: private}]\n")
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if p != nil || err == nil || !strings.Contains(err.Error(), "unsupported native suite evaluator") {
		t.Fatalf("wrong offline refusal: %v", err)
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: first\ninput: private\nexpectContains: [yes]\n")
	p, err = PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if p != nil || !errors.Is(err, errSuitePassingFile) {
		t.Fatalf("output not refused offline: %v", err)
	}
}

func TestAgentSessionsEvaluationRefusesBeforeOutputAndOrkaPreflight(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	member := filepath.Join(opt.Workspace, "agents/greeter/member.yaml")
	data, err := os.ReadFile(member)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, member, strings.Split(string(data), "\nruntime:")[0]+"\nruntime: {id: agentsessions, version: "+workspace.AgentSessionsVersion+", engine: chat}\n")
	// Invalid output would produce a different error if export preflight ran.
	opt.Out = filepath.Join(opt.Workspace, "forbidden.json")
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if p != nil || err == nil || !strings.Contains(err.Error(), "unsupported native suite runtime") {
		if p != nil {
			p.Close()
		}
		t.Fatalf("runtime reached output preparation: %v", err)
	}
	a, _, _, _ := nativeEvaluationFixture(t)
	good := nativeEvaluationOptions(t)
	p, err = PrepareSuiteWorkspaceEvaluation(t.Context(), good)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.suite.Runtime = workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}
	a.captureCommand = func(context.Context, *exec.Cmd) error { t.Fatal("unsupported runtime reached subprocess"); return nil }
	a.Cfg.KubeContext = p.options.ToContext
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err == nil || p.started {
		t.Fatalf("runtime consumed evaluation authority: %v", err)
	}
	if _, err := os.Stat(good.Out); !os.IsNotExist(err) {
		t.Fatal("unsupported runtime exported evidence", err)
	}
}

type suiteSummaryObserver func([]byte) (int, error)

func (w suiteSummaryObserver) Write(p []byte) (int, error) { return w(p) }

type suiteCaseSummaryFailWriter struct{ needle string }

func (w suiteCaseSummaryFailWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(w.needle)) {
		return 0, errors.New(nativeCanary)
	}
	return len(p), nil
}

func TestSuiteWorkspaceEvaluationPipelineStopsAndExports(t *testing.T) {
	// Missing final/source/output checks or stale guard inheritance would send
	// later cases or export a receipt that no longer attributes the original S.
	for _, stage := range []string{"passing", "first fail", "guard source", "final source", "final native", "output", "case output", "published output", "notice", "guard refusal", "kubectl", "result prerequisite"} {
		t.Run(stage, func(t *testing.T) {
			a, _, f, _ := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort = f.opt.ResultPort
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for _, c := range p.inventory {
				f.answers[c.Case.Input] = nativeCanary + " answer"
			}
			if stage == "first fail" {
				f.answers[p.inventory[0].Case.Input] = "wrong"
			}
			if stage == "output" {
				a.Out = evaluationDiffFailWriter{}
			}
			if stage == "case output" {
				a.Out = suiteCaseSummaryFailWriter{needle: "first: pass"}
			}
			if stage == "published output" {
				a.Out = suiteCaseSummaryFailWriter{needle: "Passing native receipt"}
			}
			if stage == "passing" {
				a.Cfg.Confirm = "kind-test"
			}
			if stage == "notice" {
				a.Err = evaluationDiffFailWriter{}
			}
			a.guarded = true
			guards, creates, pairWrites, finalChecks := 0, 0, 0, 0
			finalRun := false
			if stage == "final native" {
				a.Out = suiteSummaryObserver(func(raw []byte) (int, error) {
					n, err := f.out.Write(raw)
					if bytes.Contains(raw, []byte("second: pass")) {
						finalRun = true
					}
					return n, err
				})
			}
			if stage == "kubectl" {
				t.Setenv("PATH", t.TempDir())
			}
			if stage == "guard refusal" {
				a.Stdin = nil
				a.Cfg.Confirm = ""
				a.InvocationCommand = "kmx eval --workspace 'workspace with space' --to-context kind-test --inference provider:inference --out 'a'\"'\"'b.json'"
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "config") {
					guards++
					if stage == "passing" || stage == "guard refusal" {
						return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"current-context": "kind-test", "clusters": []any{map[string]any{"name": "kind-test", "cluster": map[string]any{"server": "https://remote.example.invalid"}}}, "contexts": []any{map[string]any{"name": "kind-test", "context": map[string]any{"cluster": "kind-test"}}}})
					}
					if stage == "guard source" {
						writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: changed\ninput: private\nexpectContains: [yes]\n")
					}
				}
				if slices.Contains(cmd.Args, "-f") && !slices.Contains(cmd.Args, "--dry-run=server") {
					raw, err := io.ReadAll(cmd.Stdin)
					if err != nil {
						return err
					}
					cmd.Stdin = bytes.NewReader(raw)
					var doc map[string]any
					_ = json.Unmarshal(raw, &doc)
					if doc["kind"] == "Provider" || doc["kind"] == "Agent" {
						pairWrites++
					}
					if doc["kind"] == "Task" {
						creates++
						if creates == 2 && stage == "final source" {
							writeFile(t, filepath.Join(opt.Workspace, "eval/second.yaml"), "id: changed\ninput: private\nexpectContains: [yes]\n")
						}
					}
				}
				if stage == "final native" && finalRun && slices.Contains(cmd.Args, "inference") {
					finalChecks++
					return errors.New(nativeCanary)
				}
				if stage == "result prerequisite" && slices.Contains(cmd.Args, orkaResultAccount) {
					return errors.New(nativeCanary)
				}
				return capture(ctx, cmd)
			}
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			wantCreates := 2
			if stage == "first fail" || stage == "case output" {
				wantCreates = 1
			}
			if stage == "guard source" || stage == "output" || stage == "notice" || stage == "guard refusal" || stage == "kubectl" || stage == "result prerequisite" {
				wantCreates = 0
			}
			wantGuards := 1
			if stage == "kubectl" || stage == "result prerequisite" || stage == "notice" {
				wantGuards = 0
			}
			if guards != wantGuards || creates != wantCreates {
				t.Fatalf("guard/create order: %d/%d want 1/%d: %v", guards, creates, wantCreates, err)
			}
			if stage == "guard source" || stage == "guard refusal" || stage == "kubectl" || stage == "result prerequisite" {
				if pairWrites != 0 {
					t.Fatalf("pre-write refusal changed pair: %d", pairWrites)
				}
			}
			if stage == "kubectl" || stage == "result prerequisite" {
				if err == nil || !strings.Contains(err.Error(), "refused before native changes") || strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("wrong pre-write failure phase: %v", err)
				}
			}
			if stage == "guard refusal" {
				if err == nil || !strings.Contains(err.Error(), "KAIMAHI_CONFIRM=kind-test "+a.InvocationCommand) || strings.Contains(err.Error(), "kmx --context") || strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("operator recovery advice lost: %v", err)
				}
			}
			if stage == "first fail" && (err == nil || !strings.Contains(err.Error(), "case failed") || strings.Contains(err.Error(), "outcome unknown")) {
				t.Fatalf("known failure mislabeled: %v", err)
			}
			if stage == "final native" && (!finalRun || finalChecks != 1 || !strings.Contains(f.out.String(), "first: pass") || !strings.Contains(f.out.String(), "second: pass")) {
				t.Fatalf("final run attribution not exercised: final=%t checks=%d", finalRun, finalChecks)
			}
			_, statErr := os.Stat(opt.Out)
			if stage == "passing" {
				if err != nil || statErr != nil {
					t.Fatalf("passing export: %v %v", err, statErr)
				}
				r, err := readSuitePassingReceipt(t.Context(), opt.Out)
				if err != nil || len(r.Cases) != 2 || r.Cases[0].ID != "first" || r.Cases[1].ID != "second" || r.SourceFingerprint != p.prepared.Fingerprint || r.Subject.SuiteDigest != p.prepared.Descriptor.Digest.String() {
					t.Fatalf("ordered original receipt: %v", err)
				}
				if !strings.Contains(a.Err.(*bytes.Buffer).String(), "execute 2 evaluation Tasks") {
					t.Fatal("consent omitted Task execution")
				}
				t.Chdir(opt.Workspace)
				pushed, pushErr := a.PushSuiteWorkspaceEvaluated(t.Context(), "", "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, opt.Out)
				if pushErr != nil || pushed.Descriptor.Digest != p.prepared.Descriptor.Digest {
					t.Fatalf("receipt-file discovery publication: %v", pushErr)
				}
				if strings.Count(a.Err.(*bytes.Buffer).String(), "full effective authority") != 1 {
					t.Fatal("authority notice not once")
				}
			} else if stage == "published output" {
				if err == nil || !strings.Contains(err.Error(), "output may exist") || statErr != nil {
					t.Fatalf("late output lost honest publication state: %v %v", err, statErr)
				}
			} else if err == nil || !os.IsNotExist(statErr) {
				t.Fatalf("failure exported: %v %v", err, statErr)
			}
			if strings.Contains(fmt.Sprint(err), nativeCanary) || strings.Contains(f.out.String(), nativeCanary) {
				t.Fatal("pipeline leaked payload")
			}
		})
	}
}

func TestSuiteNativeEvaluationAdmittedFrameAndOneShot(t *testing.T) {
	// Omitting either admission check would execute or credit a changed prompt/ref.
	for _, stage := range []string{"passing", "dry prompt", "create prompt", "create ref", "create override", "ambiguous", "uid", "generation", "failed", "cancelled", "result", "blank", "placement"} {
		t.Run(stage, func(t *testing.T) {
			a, placement, frame, f := nativeEvaluationFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			reached := false
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "-f") {
					raw, err := io.ReadAll(cmd.Stdin)
					if err != nil {
						return err
					}
					cmd.Stdin = bytes.NewReader(raw)
					var doc map[string]any
					_ = json.Unmarshal(raw, &doc)
					if doc["kind"] == "Task" {
						dry := slices.Contains(cmd.Args, "--dry-run=server")
						reached = true
						if stage == "ambiguous" && !dry {
							f.resultStatus = 500
						}
						var response bytes.Buffer
						out := cmd.Stdout
						cmd.Stdout = &response
						err = capture(ctx, cmd)
						cmd.Stdout = out
						if err != nil {
							return err
						}
						if stage == "ambiguous" && !dry {
							return fmt.Errorf("private ambiguous %s", nativeCanary)
						}
						_ = json.Unmarshal(response.Bytes(), &doc)
						spec := doc["spec"].(map[string]any)
						if stage == "dry prompt" && dry || stage == "create prompt" && !dry {
							spec["prompt"] = "different"
						}
						if stage == "create ref" && !dry {
							spec["agentRef"].(map[string]any)["name"] = "other"
						}
						if stage == "create override" && !dry {
							spec["ai"] = map[string]any{"model": "override"}
						}
						// These are the actual top-level v0.2.0 CRD defaults, not overrides.
						for k, v := range map[string]any{"priority": 500, "concurrencyPolicy": "Forbid", "failedRunsHistoryLimit": 1, "successfulRunsHistoryLimit": 3, "startingDeadlineSeconds": 100} {
							spec[k] = v
						}
						return json.NewEncoder(out).Encode(doc)
					}
				}
				if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "tasks.core.orka.ai") && !slices.Contains(cmd.Args, "name") {
					var response bytes.Buffer
					out := cmd.Stdout
					cmd.Stdout = &response
					err := capture(ctx, cmd)
					cmd.Stdout = out
					if err != nil {
						return err
					}
					var doc map[string]any
					_ = json.Unmarshal(response.Bytes(), &doc)
					if stage == "uid" {
						nativeMetadata(doc)["uid"] = "replacement"
					}
					if stage == "generation" {
						nativeMetadata(doc)["generation"] = 2
					}
					if stage == "failed" {
						doc["status"].(map[string]any)["phase"] = "Failed"
					}
					if stage == "cancelled" {
						cancel()
						return context.Canceled
					}
					return json.NewEncoder(out).Encode(doc)
				}
				return capture(ctx, cmd)
			}
			if stage == "result" {
				f.resultStatus = 500
			}
			if stage == "blank" {
				f.answers[frame.Case.Input] = " "
			}
			if stage == "placement" {
				placement.Agent.UID = "other"
			}
			observation, err := a.evaluateSuiteNative(ctx, placement, frame, f.opt.ResultPort, nil)
			creates := 0
			for _, call := range orkaCalls(t, f.dir) {
				if call.Document != nil && call.Document["kind"] == "Task" && slices.Contains(call.Args, "create") && !slices.Contains(call.Args, "--dry-run=server") {
					creates++
					spec := call.Document["spec"].(map[string]any)
					if spec["prompt"] != frame.Case.Input || spec["type"] != "ai" || len(spec) != 4 {
						t.Fatal("Task send changed authored behavior")
					}
					ref := spec["agentRef"].(map[string]any)
					if ref["name"] != placement.Subject.Member || ref["namespace"] != OrkaNamespace {
						t.Fatal("Task sent to wrong member")
					}
				}
			}
			wantCreates := 1
			if stage == "dry prompt" || stage == "placement" {
				wantCreates = 0
			}
			if creates != wantCreates {
				t.Fatalf("persistent Task creates=%d want %d", creates, wantCreates)
			}
			if stage == "passing" {
				if err != nil || observation.Verdict != agentruntime.EvaluationPass || observation.TaskUID == "" || observation.CaseDigest != frame.Metadata.CaseDigest || observation.AnswerSHA256 != agentruntime.EvaluationAnswerDigest(nativeCanary+" answer") {
					t.Fatalf("passing observation: %+v %v", observation, err)
				}
			} else if err == nil || observation.Verdict == agentruntime.EvaluationPass {
				t.Fatalf("unsafe result credited: %+v %v", observation, err)
			}
			if stage != "placement" && !reached {
				t.Fatal("Task boundary not reached")
			}
			if stage == "cancelled" || stage == "blank" {
				identity := context.Canceled
				if stage == "blank" {
					identity = context.DeadlineExceeded
				}
				if !errors.Is(err, identity) || !strings.Contains(err.Error(), "resources may remain") {
					t.Fatalf("post-Task cancellation identity/residue lost: %v", err)
				}
			}
			if stage == "failed" && (!strings.Contains(err.Error(), "case failed") || strings.Contains(err.Error(), "outcome unknown") || observation.Verdict != agentruntime.EvaluationFail) {
				t.Fatalf("terminal failure mislabeled: %v", err)
			}
			if strings.Contains(fmt.Sprint(err), nativeCanary) || strings.Contains(f.out.String(), nativeCanary) || strings.Contains(a.Err.(*bytes.Buffer).String(), nativeCanary) {
				t.Fatal("private payload escaped")
			}
		})
	}
}
