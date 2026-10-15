package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Use the real passing constructors; no second receipt schema in tests.
func suiteDiffFixtures(t *testing.T) map[string]*suitePassingReceipt {
	t.Helper()
	_, prepared, _, placement, observations := passingFixture(t)
	orka, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _, _ := suiteKagentFixture(t, "")
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
	kagent, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*suitePassingReceipt{"native-orka": orka, "native-kagent": kagent}
}

func TestNativeSuiteEvaluationDiff(t *testing.T) {
	fixtures := suiteDiffFixtures(t)
	for mode, before := range fixtures {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				change func(*suitePassingReceipt)
				want   string
			}{
				{"fresh execution", func(r *suitePassingReceipt) {
					r.RunID = strings.Repeat("e", 32)
					r.Cases[0].AnswerDigest = agentruntime.EvaluationAnswerDigest("different private answer")
					if r.ExecutionMode == "native-orka" {
						r.Cases[0].TaskName, r.Cases[0].TaskUID = "fresh-task", "fresh-uid"
					} else {
						r.Cases[0].KagentExecution.TaskID = "fresh-task"
					}
				}, ""},
				{"cache spelling", func(r *suitePassingReceipt) { r.SourceFingerprint = "sha256:" + strings.Repeat("f", 64) }, ""},
				{"prepared subject", func(r *suitePassingReceipt) {
					if r.Kagent == nil {
						r.Subject.Model = "other-model"
					} else {
						r.Kagent.Observation.Subject.Model = "other-model"
					}
				}, "incompatible"},
				{"owned identity", func(r *suitePassingReceipt) {
					if r.Kagent == nil {
						r.Agent.UID = "other-agent"
					} else {
						r.Kagent.Observation.Resources[1].UID = "other-agent"
					}
				}, "incompatible"},
				{"case inventory", func(r *suitePassingReceipt) { r.Cases[0].InputDigest = strings.Repeat("e", 64) }, "incompatible"},
				{"ordered inventory", func(r *suitePassingReceipt) { r.Cases[0], r.Cases[1] = r.Cases[1], r.Cases[0] }, "incompatible"},
				{"assertion inventory", func(r *suitePassingReceipt) { r.Cases[0].Assertions[0].DefinitionDigest = strings.Repeat("e", 64) }, "incompatible"},
				{"incomplete", func(r *suitePassingReceipt) { r.Cases = nil }, "invalid evaluation receipt"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw, _ := json.Marshal(before)
					after, err := parseSuitePassingReceipt(raw)
					if err != nil {
						t.Fatal(err)
					}
					tc.change(after)
					out, err := diffExecute(t, before, after)
					if tc.want == "" {
						if err != nil || !strings.Contains(out, "	stable	") || !strings.Contains(out, "comparison: complete (not an evaluation-pass claim)") || !strings.Contains(out, "before: suite ") || strings.Contains(out, "portableDigest:") {
							t.Fatalf("same-runtime assertion comparison: %v\n%s", err, out)
						}
					} else if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(out, "	stable	") {
						t.Fatalf("refusal %s: %v\n%s", tc.want, err, out)
					}
					for _, private := range []string{nativeCanary, "different private answer", "Say four", "Answer privately"} {
						if strings.Contains(out, private) {
							t.Fatalf("content escaped: %q", private)
						}
					}
				})
			}
		})
	}
	for _, pair := range [][2]any{
		{fixtures["native-orka"], fixtures["native-kagent"]},
		{fixtures["native-kagent"], fixtures["native-orka"]},
	} {
		out, err := diffExecute(t, pair[0], pair[1])
		if err == nil || err.Error() != "evaluation comparison is incompatible" || !strings.Contains(out, "comparison incompatible:") || strings.Contains(out, "	stable	") {
			t.Fatalf("family/runtime boundary: %v\n%s", err, out)
		}
	}
}

// Exercise the actual root route with trusted App-generated fixtures. An
// unusable KMX_HOME makes accidental operational configuration fail closed.
func TestNativeSuiteEvaluationDiffRootCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "kmx")
	build := exec.Command("go", "build", "-o", binary, "./cmd/kmx")
	build.Dir = "../../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build root CLI: %v\n%s", err, out)
	}
	// Native fake fixtures intentionally replace PATH with controlled tools.
	fixtures := suiteDiffFixtures(t)
	home := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(home, []byte("unusable configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	orka, kagent := diffWrite(t, fixtures["native-orka"]), diffWrite(t, fixtures["native-kagent"])
	for _, tc := range []struct {
		name, before, after string
		incompatible        bool
	}{
		{"Orka", orka, orka, false},
		{"kagent", kagent, kagent, false},
		{"mixed", orka, kagent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command(binary, "eval", "diff", tc.before, tc.after)
			command.Env = append(os.Environ(), "KMX_HOME="+home)
			out, err := command.CombinedOutput()
			if tc.incompatible {
				if err == nil || !bytes.Contains(out, []byte("evaluation comparison is incompatible")) || bytes.Contains(out, []byte("\tstable\t")) {
					t.Fatalf("mixed runtime root refusal: %v\n%s", err, out)
				}
			} else if err != nil || !bytes.Contains(out, []byte("comparison: complete (not an evaluation-pass claim)")) {
				t.Fatalf("offline root route: %v\n%s", err, out)
			}
		})
	}
}

func TestNativeSuiteEvaluationDiffUsesStrictPrivateReader(t *testing.T) {
	for mode, receipt := range suiteDiffFixtures(t) {
		t.Run(mode, func(t *testing.T) {
			valid := diffWrite(t, receipt)
			for _, tc := range []struct {
				name   string
				change func([]byte) []byte
				public bool
			}{
				{"unknown", func(raw []byte) []byte {
					return bytes.Replace(raw, []byte("{"), []byte(`{"privateOperand":"PRIVATE_DIFF_CANARY",`), 1)
				}, false},
				{"duplicate", func(raw []byte) []byte {
					return bytes.Replace(raw, []byte("{"), []byte(`{"domain":"kmx.native-suite.passing/v1",`), 1)
				}, false},
				{"null", func(raw []byte) []byte {
					return bytes.Replace(raw, []byte(`"cases":[`), []byte(`"cases":null,"removed":[`), 1)
				}, false},
				{"public file", func(raw []byte) []byte { return raw }, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw, _ := json.Marshal(receipt)
					file := diffWriteRaw(t, tc.change(raw))
					if tc.public {
						if err := os.Chmod(file, 0644); err != nil {
							t.Fatal(err)
						}
					}
					var out bytes.Buffer
					err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: valid, After: file})
					if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 || strings.Contains(err.Error(), "PRIVATE_DIFF_CANARY") {
						t.Fatalf("strict private refusal: %v %s", err, &out)
					}
				})
			}
		})
	}
}

func TestNativeSuiteEvaluationDiffNamesPreviousLayoutRemedy(t *testing.T) {
	for mode, receipt := range suiteDiffFixtures(t) {
		t.Run(mode, func(t *testing.T) {
			valid := diffWrite(t, receipt)
			old := *receipt
			old.Cases = append([]suitePassingCase(nil), receipt.Cases...)
			old.Cases[0].Path = "metadata/kmx/eval/example.yaml"
			raw, err := json.Marshal(&old)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: valid, After: diffWriteRaw(t, raw)})
			if !errors.Is(err, errPreviousSuiteReceipt) || out.Len() != 0 {
				t.Fatalf("previous layout diff error = %v", err)
			}
		})
	}
}
