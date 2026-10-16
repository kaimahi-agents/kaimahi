package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Losing a runtime branch, identity uniqueness or offline rendering check must
// reject serialized evidence, not authorize publication of an unchanged S.
func TestSuiteKagentPassingReceiptRiskMutations(t *testing.T) {
	fixtures := suiteDiffFixtures(t)
	opt := suiteKagentEvaluationOptions(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	valid, err := canonicalSuitePassingReceipt(t.Context(), fixtures["native-kagent"], p.suite)
	if err != nil {
		t.Fatal("real fixture coherence", err)
	}
	for _, tc := range []struct {
		name          string
		change        func(*suitePassingReceipt)
		coherenceOnly bool
	}{
		{"mixed runtime", func(r *suitePassingReceipt) {
			r.ExecutionMode = "native-orka"
			r.Subject, r.Provider, r.Agent = fixtures["native-orka"].Subject, fixtures["native-orka"].Provider, fixtures["native-orka"].Agent
		}, false},
		{"Orka Task alias", func(r *suitePassingReceipt) { r.Cases[0].TaskUID = "orka-task" }, false},
		{"duplicate RPC", func(r *suitePassingReceipt) { r.Cases[1].KagentExecution.RPCID = r.Cases[0].KagentExecution.RPCID }, false},
		{"duplicate message", func(r *suitePassingReceipt) {
			r.Cases[1].KagentExecution.RequestMessageID = r.Cases[0].KagentExecution.RequestMessageID
		}, false},
		{"duplicate Task", func(r *suitePassingReceipt) { r.Cases[1].KagentExecution.TaskID = r.Cases[0].KagentExecution.TaskID }, false},
		{"duplicate context", func(r *suitePassingReceipt) {
			r.Cases[1].KagentExecution.ContextID = r.Cases[0].KagentExecution.ContextID
		}, false},
		{"failed", func(r *suitePassingReceipt) { r.Cases[0].KagentExecution.State = "failed" }, false},
		{"multiple turns", func(r *suitePassingReceipt) { r.Cases[0].KagentExecution.UserTurns = 2 }, false},
		{"profile", func(r *suitePassingReceipt) { r.Kagent.Observation.Subject.Profile.Version = "v0.10.2" }, false},
		{"source pin", func(r *suitePassingReceipt) { r.Kagent.SourceCommit = strings.Repeat("a", 40) }, false},
		{"image pin", func(r *suitePassingReceipt) { r.Kagent.Observation.Images[0] = kagentEvaluationImage{} }, false},
		{"binding consumed", func(r *suitePassingReceipt) {
			r.Kagent.Observation.Subject.Binding.ModelConfig.BaseURL = "https://other.invalid"
		}, true},
		{"regenerated payload", func(r *suitePassingReceipt) { r.Kagent.Observation.Subject.PayloadDigest = strings.Repeat("0", 64) }, true},
		{"resource render", func(r *suitePassingReceipt) { r.Kagent.Observation.Resources[0].SpecDigest = strings.Repeat("0", 64) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseSuitePassingReceipt(valid)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(r)
			basic := basicSuitePassingReceipt(r)
			if tc.coherenceOnly {
				if basic != nil {
					t.Fatal("coherence case failed basic validation", basic)
				}
			} else if !errors.Is(basic, errSuitePassingReceipt) {
				t.Fatalf("invalid branch accepted: %v", basic)
			}
			if _, err := canonicalSuitePassingReceipt(t.Context(), r, p.suite); !errors.Is(err, errSuitePassingReceipt) {
				t.Fatalf("canonical accepted mutation: %v", err)
			}
			raw, err := agentsuite.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseSuitePassingReceipt(raw)
			if !tc.coherenceOnly && !errors.Is(err, errSuitePassingReceipt) {
				t.Fatalf("serialized invalid evidence accepted: %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"unknown", "{", `{"unknown":true,`},
		{"foreign branch null", "{", `{"subject":null,`},
		{"scalar null", `"userTurns":1`, `"userTurns":null`},
		{"image inventory null", `"requiredImages":[]`, `"requiredImages":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(valid, []byte(tc.old), []byte(tc.replacement), 1)
			if bytes.Equal(raw, valid) {
				t.Fatal("mutation not applied")
			}
			if _, err := parseSuitePassingReceipt(raw); !errors.Is(err, errSuitePassingReceipt) {
				t.Fatalf("strict reader accepted mutation: %v", err)
			}
		})
	}
}

func TestReleaseLiftSelectorRuntimeFencesBeforeCluster(t *testing.T) {
	orka, _, oo, _ := releaseLiftFixture(t)
	kg, ko, _, _ := releaseKagentFixture(t)
	// Successful controls establish coherent real S/E, selectors and configured Apps.
	for _, pair := range []struct {
		a *App
		o ReleaseLiftOptions
	}{{orka, oo}, {kg, ko}} {
		if _, err := pair.a.PrepareReleaseLift(t.Context(), pair.o); err != nil {
			t.Fatal("invalid runtime fence control", err)
		}
	}
	calls := 0
	trap := func(context.Context, *exec.Cmd) error {
		calls++
		return errors.New("unexpected cluster/guard/admission/native call")
	}
	orka.captureCommand, kg.captureCommand = trap, trap
	for _, tc := range []struct {
		name    string
		a       *App
		opt     ReleaseLiftOptions
		offline bool
		change  func(*ReleaseLiftOptions)
	}{
		{"both selectors", kg, ko, true, func(o *ReleaseLiftOptions) { o.Inference = oo.Inference }},
		{"neither selector", kg, ko, true, func(o *ReleaseLiftOptions) { o.Bindings = "" }},
		{"KG package with inference", kg, ko, false, func(o *ReleaseLiftOptions) { o.Bindings, o.Inference = "", oo.Inference }},
		{"Orka package with bindings", orka, oo, false, func(o *ReleaseLiftOptions) { o.Inference, o.Bindings = "", ko.Bindings }},
		{"Orka E with KG package", kg, ko, false, func(o *ReleaseLiftOptions) { o.Evidence = oo.Evidence }},
		{"KG E with Orka package", orka, oo, false, func(o *ReleaseLiftOptions) { o.Evidence = ko.Evidence }},
		{"explicit namespace disagrees with bindings", kg, ko, false, func(o *ReleaseLiftOptions) { o.ToNamespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.opt
			tc.change(&bad)
			e := ValidateReleaseLiftOptions(bad)
			if tc.offline && !errors.Is(e, ErrReleaseLift) || !tc.offline && e != nil {
				t.Fatalf("wrong offline selector boundary: %v", e)
			}
			calls = 0
			p, err := tc.a.PrepareReleaseLift(t.Context(), bad)
			if p != nil || !errors.Is(err, ErrReleaseLift) || calls != 0 {
				t.Fatalf("runtime/selector fence: plan=%v err=%v calls=%d", p, err, calls)
			}
		})
	}
}

func TestSuiteKagentPushRequiresCoherence(t *testing.T) {
	r := suiteDiffFixtures(t)["native-kagent"]
	raw, err := agentsuite.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	opt := suiteKagentEvaluationOptions(t)
	for _, change := range []string{"passing", "incoherent", "other S", "agentsessions"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			source := filepath.Join(t.TempDir(), "workspace")
			copyTree(t, opt.Workspace, source)
			evidence := diffWriteRaw(t, raw)
			if change == "incoherent" {
				bad, err := parseSuitePassingReceipt(raw)
				if err != nil {
					t.Fatal(err)
				}
				bad.Kagent.Observation.Subject.PayloadDigest = strings.Repeat("0", 64)
				evidence = diffWrite(t, bad)
			}
			if change == "other S" {
				writeFile(t, filepath.Join(source, "eval/example.yaml"), "id: first\ninput: changed\nexpectContains: [answer]\n")
			}
			if change == "agentsessions" {
				member := filepath.Join(source, "agents/sample/member.yaml")
				data, err := os.ReadFile(member)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, member, strings.Split(string(data), "\nruntime:")[0]+"\nruntime: {id: agentsessions, version: "+workspace.AgentSessionsVersion+", engine: chat}\n")
			}
			destination := filepath.Join(t.TempDir(), "layout")
			result, err := (&App{Out: io.Discard}).pushSuiteWorkspaceEvaluated(t.Context(), source, "suite:v1", destination, false, false, SuitePushProvenance{}, evidence)
			if change == "passing" {
				if err != nil || result.Descriptor.Digest.String() != r.suiteDigest() {
					t.Fatalf("same-runtime exact-S publication: %v", err)
				}
			} else {
				if change == "incoherent" && !errors.Is(err, errSuitePassingReceipt) {
					t.Fatalf("wrong coherence refusal: %v", err)
				}
				if (change == "other S" || change == "agentsessions") && (err == nil || !strings.Contains(err.Error(), "expected publication identity")) {
					t.Fatalf("wrong exact-S refusal: %v", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("refused evidence transferred artifact")
				}
			}
		})
	}
}
