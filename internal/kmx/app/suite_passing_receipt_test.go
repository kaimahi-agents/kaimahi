package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func passingFixture(t *testing.T) (string, *workspace.Prepared, *workspace.NativeSuite, *suiteNativePlacement, []suitePassingObservation) {
	t.Helper()
	_, root, _ := gitWorkspaceRepository(t, false)
	writeFile(t, filepath.Join(root, "eval/second.yaml"), "id: second\ninput: Say four\nassertions:\n- {id: positive, type: contains, value: '4'}\n- {id: negative, type: notContains, value: refuse}\n")
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := nativeFixture(t)
	native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	placement := &suiteNativePlacement{Subject: native.Subject, Provider: suiteNativeOwnedResource{"owned-provider", 1, agentruntime.EvaluationAnswerDigest("admitted provider spec")}, Agent: suiteNativeOwnedResource{"owned-agent", 1, agentruntime.EvaluationAnswerDigest("admitted agent spec")}}
	observations := make([]suitePassingObservation, 0, len(suite.Cases))
	for _, file := range suite.Cases {
		c, err := agentruntime.ParseEvaluationCase(file.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		rows := agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: "4"})
		for i := range rows {
			rows[i].Reason = nativeCanary
		}
		observations = append(observations, suitePassingObservation{CaseDigest: agentruntime.EvaluationCaseDigest(file.Bytes), InputDigest: agentruntime.EvaluationInputDigest(c.Input), EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationPass, TaskName: "task-" + c.ID, TaskUID: "uid-" + c.ID, AnswerSHA256: agentruntime.EvaluationAnswerDigest("4"), Assertions: rows, Matched: []string{nativeCanary}}})
	}
	return root, prepared, suite, placement, observations
}

type refusingSuiteEntropy struct{ calls int }

func (r *refusingSuiteEntropy) Read(p []byte) (int, error) { r.calls++; return len(p), nil }

func TestSuitePassingReceiptRefusesOtherRuntimesBeforeFabrication(t *testing.T) {
	for _, declaration := range []string{"{id: kagent, version: v0.10.3, engine: go}", "{id: agentsessions, version: " + workspace.AgentSessionsVersion + ", engine: chat}"} {
		t.Run(declaration, func(t *testing.T) {
			root, _, orka, placement, observations := passingFixture(t)
			member := filepath.Join(root, "agents/greeter/member.yaml")
			raw, err := os.ReadFile(member)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, member, strings.Split(string(raw), "\nruntime:")[0]+"\nruntime: "+declaration+"\n")
			prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			suite, err := workspace.Load(t.Context(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			// All other identities remain coherent, so only the runtime guard can refuse.
			orka.Runtime = suite.Runtime
			if err := coherentSuitePassingSubject(placement.Subject, orka); !errors.Is(err, errSuitePassingReceipt) {
				t.Errorf("coherence accepted another runtime: %v", err)
			}
			entropy := &refusingSuiteEntropy{}
			previous := rand.Reader
			rand.Reader = entropy
			t.Cleanup(func() { rand.Reader = previous })
			receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
			if !errors.Is(err, errSuitePassingReceipt) || receipt != nil || entropy.calls != 0 {
				t.Fatalf("constructed Orka receipt or consumed RNG: %v, reads %d", err, entropy.calls)
			}
		})
	}
}

func TestOrkaEvidenceCannotAuthorizeOtherRuntimeWorkspace(t *testing.T) {
	for _, declaration := range []string{"{id: kagent, version: v0.10.3, engine: go}", "{id: agentsessions, version: " + workspace.AgentSessionsVersion + ", engine: chat}"} {
		t.Run(declaration, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			root, prepared, suite, placement, observations := passingFixture(t)
			receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(privateSuiteDir(t), "passing.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			member := filepath.Join(root, "agents/greeter/member.yaml")
			data, err := os.ReadFile(member)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, member, strings.Split(string(data), "\nruntime:")[0]+"\nruntime: "+declaration+"\n")
			for _, operation := range []string{"eval", "push --evaluation"} {
				t.Run(operation, func(t *testing.T) {
					destination := filepath.Join(privateSuiteDir(t), "output")
					if operation == "eval" {
						p, prepareErr := PrepareSuiteWorkspaceEvaluation(t.Context(), SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "kind-target", Inference: "provider:inference", Out: destination})
						if p != nil {
							p.Close()
							t.Fatal("another runtime reached Orka evaluation preparation")
						}
						err = prepareErr
					} else {
						var out bytes.Buffer
						a := &App{Out: &out, Err: &out}
						_, err = a.PushSuiteWorkspaceEvaluated(t.Context(), root, "suite:v1", destination, false, false, SuitePushProvenance{}, file)
						if out.Len() != 0 {
							t.Fatal("unsupported runtime emitted publication output")
						}
					}
					if err == nil {
						t.Fatalf("wrong runtime refusal: %v", err)
					}
					if _, err := os.Stat(destination); !os.IsNotExist(err) {
						t.Fatal("unsupported runtime published output")
					}
				})
			}
		})
	}
}

func TestSuitePassingReceiptRejectsIncompleteObservations(t *testing.T) {
	for _, change := range []string{"fail", "unknown", "no task", "empty answer", "empty answer hash", "wrong case", "assertion fail", "incomplete placement", "case frame", "input frame", "unavailable detail", "owned uid collision", "repeated name", "repeated uid", "missing case", "case order", "assertion order", "canceled"} {
		t.Run(change, func(t *testing.T) {
			_, prepared, _, placement, observations := passingFixture(t)
			ctx := t.Context()
			switch change {
			case "fail":
				observations[0].Verdict = agentruntime.EvaluationFail
			case "unknown":
				observations[0].Verdict = agentruntime.EvaluationUnknown
			case "no task":
				observations[0].TaskUID = ""
			case "empty answer":
				observations[0].AnswerSHA256 = ""
			case "empty answer hash":
				observations[0].AnswerSHA256 = agentruntime.EvaluationAnswerDigest("")
			case "wrong case":
				observations[0].CaseID = "other"
			case "assertion fail":
				observations[0].Assertions[0].Verdict = agentruntime.EvaluationFail
			case "incomplete placement":
				placement.Agent.UID = ""
			case "case frame":
				observations[0].CaseDigest = agentruntime.EvaluationCaseDigest([]byte("other"))
			case "input frame":
				observations[0].InputDigest = agentruntime.EvaluationInputDigest("other")
			case "unavailable detail":
				observations[0].Detail = evaluationResultUnavailable
			case "owned uid collision":
				placement.Agent.UID = placement.Provider.UID
			case "repeated name":
				observations[1].TaskName = observations[0].TaskName
			case "repeated uid":
				observations[1].TaskUID = observations[0].TaskUID
			case "missing case":
				observations = observations[:1]
			case "case order":
				observations[0], observations[1] = observations[1], observations[0]
			case "assertion order":
				rows := observations[1].Assertions
				rows[0], rows[1] = rows[1], rows[0]
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			want := errSuitePassingReceipt
			if change == "canceled" {
				want = context.Canceled
			}
			if _, err := newSuitePassingReceipt(ctx, prepared, placement, observations); !errors.Is(err, want) {
				t.Fatalf("wrong observation refusal: %v", err)
			}
		})
	}
}

func TestSuitePassingReceiptWireContract(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"unknown", "duplicate", "case", "trailing", "UTF8", "missing nested", "null scalar", "null array", "null assertion array", "credentials", "nested runID token", "oversize", "bad digest", "invalid owned", "no cases"} {
		t.Run(change, func(t *testing.T) {
			altered := string(raw)
			switch change {
			case "unknown":
				altered = strings.Replace(altered, `"schemaVersion":1`, `"schemaVersion":1,"`+nativeCanary+`":1`, 1)
			case "duplicate":
				altered = strings.Replace(altered, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)
			case "case":
				altered = strings.Replace(altered, `"schemaVersion"`, `"SchemaVersion"`, 1)
			case "trailing":
				altered += "{}"
			case "UTF8":
				altered += string([]byte{255})
			case "missing nested":
				altered = strings.Replace(altered, `"KeyPresent":true,`, "", 1)
			case "null scalar":
				altered = strings.Replace(altered, `"Generation":1`, `"Generation":null`, 1)
			case "null array":
				altered = strings.Replace(altered, `"RequiredImages":[]`, `"RequiredImages":null`, 1)
			case "null assertion array":
				var d map[string]any
				json.Unmarshal(raw, &d)
				d["cases"].([]any)[0].(map[string]any)["assertions"] = nil
				b, _ := json.Marshal(d)
				altered = string(b)
			case "credentials":
				altered = strings.Replace(altered, `"Model":"test-model"`, `"Model":"ghp_`+strings.Repeat("x", 36)+`"`, 1)
			case "nested runID token":
				altered = strings.Replace(altered, `"Model":"test-model"`, `"Model":"`+strings.Repeat("a", 32)+`"`, 1)
			case "oversize":
				altered += strings.Repeat(" ", maxSuitePassingReceiptBytes)
			case "bad digest":
				altered = strings.Replace(altered, receipt.SourceFingerprint, "sha256:bad", 1)
			case "invalid owned":
				altered = strings.Replace(altered, `"UID":"owned-agent"`, `"UID":""`, 1)
			case "no cases":
				var d map[string]any
				json.Unmarshal(raw, &d)
				d["cases"] = []any{}
				b, _ := json.Marshal(d)
				altered = string(b)
			}
			if altered == string(raw) {
				t.Fatal("mutation missed fixture")
			}
			_, err := parseSuitePassingReceipt([]byte(altered))
			if err == nil || strings.Contains(err.Error(), nativeCanary) {
				t.Fatalf("unsafe wire accepted/error: %v", err)
			}
			if change == "credentials" || change == "nested runID token" {
				if !strings.Contains(err.Error(), "credential-shaped native receipt metadata") {
					t.Fatalf("whole-document scanner not exercised: %v", err)
				}
			} else if !errors.Is(err, errSuitePassingReceipt) {
				t.Fatalf("wrong wire refusal: %v", err)
			}
		})
	}
}

func TestSuitePassingReceiptCoherence(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	original, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), original, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"S", "G", "member", "binding", "payload", "rendered", "model", "pinned", "secret ref", "source collision", "owned shape", "case raw", "input", "assertion", "evaluator"} {
		t.Run(change, func(t *testing.T) {
			r, err := parseSuitePassingReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			bad := agentruntime.EvaluationAnswerDigest("different")
			switch change {
			case "S":
				r.Subject.SuiteDigest = "sha256:" + bad
			case "G":
				r.Subject.GraphDigest = "sha256:" + bad
			case "member":
				r.Subject.MemberDigest = "sha256:" + bad
			case "binding":
				r.Subject.BindingDigest = bad
			case "payload":
				r.Subject.PayloadDigest = bad
			case "rendered":
				r.Subject.RenderedDigest = bad
			case "model":
				r.Subject.Model = "other"
			case "pinned":
				r.Subject.Runtime.AIWorkerImage = "other"
			case "secret ref":
				r.Subject.Binding.Secret.Key = "different"
			case "source collision":
				r.Subject.Binding.Provider.Name = r.Subject.Member
			case "owned shape":
				r.Agent.Generation = 0
			case "case raw":
				r.Cases[0].CaseDigest = bad
			case "input":
				r.Cases[0].InputDigest = bad
			case "assertion":
				r.Cases[0].Assertions[0].DefinitionDigest = bad
			case "evaluator":
				r.Cases[0].Assertions[0].EvaluatorVersion = "other"
			}
			if _, err := canonicalSuitePassingReceipt(t.Context(), r, suite); !errors.Is(err, errSuitePassingReceipt) {
				t.Fatalf("wrong coherence refusal: %v", err)
			}
		})
	}
}

func TestSuitePassingInventoryPreflight(t *testing.T) {
	_, _, suite, _, _ := passingFixture(t)
	for _, kind := range []string{"toolCalled", "llmJudge", "budget"} {
		t.Run(kind, func(t *testing.T) {
			modified := *suite
			if kind == "budget" {
				var text strings.Builder
				text.WriteString("id: huge\ninput: public\nassertions:\n")
				for i := 0; i < 1000; i++ {
					fmt.Fprintf(&text, "- {id: a%d, type: contains, value: public}\n", i)
				}
				modified.Cases = nil
				for i := 0; i < 6; i++ {
					data := []byte(strings.Replace(text.String(), "id: huge", fmt.Sprintf("id: huge%d", i), 1))
					modified.Cases = append(modified.Cases, workspace.PackagedCase{Path: fmt.Sprintf("metadata/kmx/eval/c%d.yaml", i), Digest: "sha256:" + agentruntime.EvaluationCaseDigest(data), Bytes: data})
				}
			} else {
				definition := "- {id: a, type: toolCalled, tool: public}"
				if kind == "llmJudge" {
					definition = "- {id: a, type: llmJudge, rubric: public, minScore: 50, judgeModel: judge, judgeVersion: v1}"
				}
				data := []byte("id: unsupported\ninput: public\nassertions:\n" + definition + "\n")
				modified.Cases = []workspace.PackagedCase{{Path: "metadata/kmx/eval/unsupported.yaml", Digest: "sha256:" + agentruntime.EvaluationCaseDigest(data), Bytes: data}}
			}
			if _, _, err := suitePassingInventory(&modified); err == nil || !strings.Contains(err.Error(), map[bool]string{true: "budget", false: "unsupported"}[kind == "budget"]) {
				t.Fatalf("missing targeted preflight refusal: %v", err)
			}
		})
	}
}

func TestSuitePassingReceiptPreservesAcceptedProviderInputs(t *testing.T) {
	for _, endpoint := range []string{"", "https://model.example.invalid/" + strings.Repeat("a/", 140)} {
		t.Run(fmt.Sprintf("endpoint length %d", len(endpoint)), func(t *testing.T) {
			root, _, _, placement, observations := passingFixture(t)
			member := filepath.Join(root, "agents/greeter/member.yaml")
			raw, err := os.ReadFile(member)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, member, strings.Replace(string(raw), "test-model", "modèle-四", 1))
			prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			suite, err := workspace.Load(t.Context(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			a, _, f := nativeFixture(t)
			f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["baseURL"] = endpoint
			native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
			if err != nil {
				t.Fatal(err)
			}
			placement.Subject = native.Subject
			if _, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations); err != nil {
				t.Fatal("accepted provider input refused", err)
			}
		})
	}
}

func TestSuitePassingReceiptProjectsOnlyPassingMetadata(t *testing.T) {
	_, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(nativeCanary)) || bytes.Contains(raw, []byte("reason")) || bytes.Contains(raw, []byte("Matched")) {
		t.Fatal("runtime payload leaked")
	}
	parsed, err := parseSuitePassingReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SourceFingerprint != prepared.Fingerprint || len(parsed.Cases) != len(suite.Cases) {
		t.Fatal("lost source or inventory identity")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc["subject"].(map[string]any)["SourceFingerprint"]; exists {
		t.Fatal("frozen subject changed")
	}
}
