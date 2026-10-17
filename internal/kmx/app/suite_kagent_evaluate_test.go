package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func suiteKagentEvaluationOptions(t *testing.T) SuiteWorkspaceEvaluationOptions {
	t.Helper()
	opt := SuiteWorkspaceEvaluationOptions{Workspace: filepath.Join(t.TempDir(), "workspace"), ToContext: "kind-test", Out: filepath.Join(privateSuiteDir(t), "passing.json")}
	// The fixture uses fixed sample child names; author that member, not a forged loaded identity.
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer privately")
	if err := workspace.Create(t.Context(), opt.Workspace, workspace.CreateOptions{Name: "sample", Model: "packaged-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: first\ninput: input\nexpectContains: [answer]\n")
	writeFile(t, filepath.Join(opt.Workspace, "eval/second.yml"), "id: second\ninput: input\nexpectContains: [answer]\n")
	b := agentruntime.KagentBindings{APIVersion: agentruntime.KagentBindingsAPIVersion, Kind: agentruntime.KagentBindingsKind, Namespace: "agents", ModelConfig: agentruntime.KagentModelConfigBindings{Provider: "openai", SecretRef: agentruntime.KagentSecretRefBindings{Name: "model-key", Key: "api-key"}}}
	raw, err := agentruntime.EncodeKagentBindings(b)
	if err != nil {
		t.Fatal(err)
	}
	opt.Bindings = filepath.Join(privateSuiteDir(t), "bindings.yaml")
	if err := os.WriteFile(opt.Bindings, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opt.Inference, opt.ToNamespace, opt.ResultPort = "", "", ""
	return opt
}

func TestSuiteKagentEvaluationPassingPipeline(t *testing.T) {
	a, _, _, dir := suiteKagentFixture(t, "")
	opt := suiteKagentEvaluationOptions(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err, a.Err)
	}
	if len(evaluationSends(t, dir)) != 2 {
		t.Fatal("not full ordered one-shot execution")
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DiffAgentEvaluations(EvaluationDiffOptions{Before: opt.Out, After: opt.Out}); err != nil {
		t.Fatal("offline suite diff", err)
	}
	if r.ExecutionMode != "native-kagent" {
		t.Fatal("runtime evidence crossed boundary")
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err == nil {
		t.Fatal("prepared invocation reused")
	}
}

// Removing any pre/post-send recheck or accepting ambiguous native outcomes
// would increase the effect counts or publish a partial passing receipt.
func TestSuiteKagentEvaluationRiskBoundaries(t *testing.T) {
	for _, stage := range []string{"trusted-proxy", "source-before", "source-after", "binding-before", "binding-after", "rebound-before", "rebound-after", "namespace", "secret", "images", "runtime-config", "auth-after", "modelconfig", "eval-post-drift", "malformed", "foreign-history", "no-history", "duplicate-task", "duplicate-context", "failed", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			a, _, _, dir := suiteKagentFixture(t, "")
			opt := suiteKagentEvaluationOptions(t)
			if strings.HasPrefix(stage, "duplicate-") {
				writeFile(t, filepath.Join(opt.Workspace, "eval/third.yaml"), "id: third\ninput: input\nexpectContains: [answer]\n")
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
			if stage == "trusted-proxy" || stage == "eval-post-drift" {
				t.Setenv("KMX_KAGENT_TEST_SCENARIO", stage)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sends, effects, mutations := 0, 0, 0
			mutate := func() {
				mutations++
				path := opt.Bindings
				if strings.HasPrefix(stage, "source-") {
					path = filepath.Join(opt.Workspace, "eval/example.yaml")
				}
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if strings.HasPrefix(stage, "rebound-") {
					next := path + ".replacement"
					if e := os.WriteFile(next, raw, 0600); e != nil {
						t.Fatal(e)
					}
					if e := os.Rename(next, path); e != nil {
						t.Fatal(e)
					}
				} else {
					writeFile(t, path, string(raw)+"# changed snapshot\n")
				}
			}
			capture := a.captureCommand
			a.captureCommand = func(c context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "create") && slices.Contains(cmd.Args, "-f") && !slices.ContainsFunc(cmd.Args, func(arg string) bool { return strings.Contains(arg, "/api/a2a/") }) {
					effects++
				}
				if stage == "namespace" && sends > 0 && slices.Contains(cmd.Args, "namespace") && slices.Contains(cmd.Args, "agents") {
					mutations++
					_, e := cmd.Stdout.Write([]byte(`{"kind":"Namespace","metadata":{"name":"agents","uid":"replacement"}}`))
					return e
				}
				dst := cmd.Stdout
				var buf bytes.Buffer
				cmd.Stdout = &buf
				e := capture(c, cmd)
				cmd.Stdout = dst
				if e != nil {
					return e
				}
				raw := buf.Bytes()
				var doc map[string]any
				_ = json.Unmarshal(raw, &doc)
				if stage == "modelconfig" && sends > 0 && doc["kind"] == "ModelConfig" {
					doc["spec"].(map[string]any)["baseURL"] = "https://changed.invalid"
					mutations++
					raw, e = json.Marshal(doc)
					if e != nil {
						return e
					}
				}
				if strings.HasSuffix(stage, "-before") && mutations == 0 && doc["kind"] == "Agent" && slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
					mutate()
				}
				result, isTask := doc["result"].(map[string]any)
				if isTask && result["kind"] == "task" {
					sends++
					switch stage {
					case "source-after", "binding-after", "rebound-after":
						if sends == 1 {
							mutate()
						}
					case "secret", "images", "runtime-config", "auth-after":
						scenario := map[string]string{"secret": "dependency-drift", "images": "wrong-agent-imageid", "runtime-config": "wrong-image-config", "auth-after": "trusted-proxy"}[stage]
						t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
						mutations++
					case "malformed":
						raw = []byte("{")
					case "foreign-history":
						result["history"].([]any)[0].(map[string]any)["messageId"] = "foreign"
					case "no-history":
						delete(result, "history")
					case "duplicate-task":
						result["id"] = "task-1"
					case "duplicate-context":
						result["contextId"] = "context-1"
					case "failed":
						result["status"].(map[string]any)["state"] = "failed"
					case "cancelled":
						cancel()
					}
					if stage != "malformed" {
						raw, e = json.Marshal(doc)
						if e != nil {
							return e
						}
					}
				}
				_, e = dst.Write(raw)
				return e
			}
			err = a.EvaluateSuiteWorkspace(ctx, p)
			wantSends := 1
			if stage == "trusted-proxy" || strings.HasSuffix(stage, "-before") {
				wantSends = 0
			}
			if strings.HasPrefix(stage, "duplicate-") {
				wantSends = 2
			}
			wantError := errSuiteNativeEvaluation
			if stage == "trusted-proxy" {
				wantError = errSuiteNativeRefused
			}
			if stage == "failed" {
				wantError = errSuiteNativeCaseFailed
			}
			if !errors.Is(err, wantError) {
				t.Fatalf("wrong native refusal: %v", err)
			}
			if stage == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if errors.Is(err, errSuiteKagentOccupied) {
				t.Fatal("unrelated refusal classified occupied")
			}
			if len(evaluationSends(t, dir)) != wantSends || sends != wantSends {
				t.Fatalf("send/retry boundary: captured=%d observed=%d want=%d", len(evaluationSends(t, dir)), sends, wantSends)
			}
			wantEffects := 4
			if stage == "trusted-proxy" {
				wantEffects = 0
			}
			if effects != wantEffects {
				t.Fatalf("admission/create count=%d want=%d", effects, wantEffects)
			}
			if (strings.Contains(stage, "before") || strings.Contains(stage, "after") || stage == "namespace" || stage == "secret" || stage == "images" || stage == "runtime-config" || stage == "modelconfig") && mutations == 0 {
				t.Fatal("drift never injected")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("refusal exported passing evidence")
			}
		})
	}
}

func TestSuiteKagentEvaluationOccupiedIsNotInspectionFailure(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "occupied", true: "inspection unavailable"}[unknown], func(t *testing.T) {
			a, _, _, dir := suiteKagentFixture(t, "")
			opt := suiteKagentEvaluationOptions(t)
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
			if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			before := len(kagentCalls(t, dir))
			opt.Out = filepath.Join(privateSuiteDir(t), "second.json")
			next, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			if unknown {
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "modelconfigs.kagent.dev") {
						return errors.New("inspection unavailable")
					}
					return capture(ctx, cmd)
				}
			}
			err = a.EvaluateSuiteWorkspace(t.Context(), next)
			wantError := errSuiteNativeEvaluation
			if unknown {
				wantError = errSuiteNativeRefused
			}
			if !errors.Is(err, wantError) || errors.Is(err, errSuiteKagentOccupied) == unknown {
				t.Fatalf("wrong occupancy classification: %v", err)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
			if len(evaluationSends(t, dir)) != 2 {
				t.Fatal("occupied/unknown target sent new Task")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("occupied/unknown target exported")
			}
		})
	}
}

func TestSuiteKagentEvaluationOfflineBindingSelection(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	opt := suiteKagentEvaluationOptions(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.options.ToNamespace != "agents" || p.suite.Runtime.ID != "kagent" || len(p.inventory) != 2 || p.inventory[1].Metadata.Path != "evals/"+p.suite.MemberID+"/cases/second.json" {
		t.Fatal("packaged runtime, binding namespace or complete inventory lost")
	}
	for _, change := range []string{"missing", "both", "namespace", "port", "link", "malformed"} {
		t.Run(change, func(t *testing.T) {
			bad := opt
			switch change {
			case "missing":
				bad.Bindings = ""
			case "both":
				bad.Inference = "provider:inference"
			case "namespace":
				bad.ToNamespace = "other"
			case "port":
				bad.ResultPort = "19180"
			case "link":
				bad.Bindings = filepath.Join(privateSuiteDir(t), "link")
				if err := os.Symlink(opt.Bindings, bad.Bindings); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				bad.Bindings = filepath.Join(privateSuiteDir(t), "bad")
				writeFile(t, bad.Bindings, "unknown: private\n")
			}
			got, err := PrepareSuiteWorkspaceEvaluation(t.Context(), bad)
			if got != nil {
				got.Close()
			}
			if err == nil {
				t.Fatal("invalid author binding accepted")
			}
		})
	}
	t.Run("placeholder", func(t *testing.T) {
		bad := opt
		bad.Bindings = filepath.Join(privateSuiteDir(t), "placeholder")
		writeFile(t, bad.Bindings, strings.Replace(string(p.bindings.raw), "    provider: openai\n", "    provider: openai\n    baseURL: https://<endpoint>/v1\n", 1))
		got, err := PrepareSuiteWorkspaceEvaluation(t.Context(), bad)
		if got != nil {
			got.Close()
		}
		if !errors.Is(err, scaffold.ErrUnresolvedPlaceholder) {
			t.Fatalf("placeholder classification lost: %v", err)
		}
	})
}
