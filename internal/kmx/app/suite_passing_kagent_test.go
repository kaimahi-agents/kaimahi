package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
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
			raw, err := spec.Marshal(r)
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

func TestSuiteKagentEvidenceCannotLiftAndPushRequiresCoherence(t *testing.T) {
	r := suiteDiffFixtures(t)["native-kagent"]
	raw, err := spec.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	evidence := diffWriteRaw(t, raw)
	plan, err := (&App{}).PrepareReleaseLift(t.Context(), ReleaseLiftOptions{Evidence: evidence})
	if plan != nil || !errors.Is(err, ErrReleaseLift) || !errors.Is(err, errReleaseLiftKagentEvidence) {
		t.Fatalf("kagent reached config/artifact/Orka consumer: %v", err)
	}
	opt := suiteKagentEvaluationOptions(t)
	for _, change := range []string{"passing", "incoherent", "other S"} {
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
				if change == "other S" && (err == nil || !strings.Contains(err.Error(), "expected publication identity")) {
					t.Fatalf("wrong exact-S refusal: %v", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("refused evidence transferred artifact")
				}
			}
		})
	}
}
