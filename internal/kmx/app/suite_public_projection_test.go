package app

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSuitePublicJudgeRepeatedProjection(t *testing.T) {
	root, _, _, placement, _ := passingFixture(t)
	writeFile(t, filepath.Join(root, "eval/example.yaml"), suiteJudgeYAML+"runs: 3\nminPasses: 2\n")
	// Reusing the same authored judge configuration across cases is ordinary.
	writeFile(t, filepath.Join(root, "eval/second.yaml"), strings.Replace(suiteJudgeYAML, "id: first", "id: second", 1))
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
	placement.Subject = native.Subject
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]suitePassingObservation, 0, len(inventory))
	for i, frame := range inventory {
		rows := agentruntime.EvaluateAssertions(frame.Case, agentruntime.EvaluationEvidence{Answer: "4", AnswerAvailable: true})
		verdict := agentruntime.EvaluationPass
		if frame.Case.Assertions[0].Type == "llmJudge" {
			scores := []int{90, 90}
			if i == 0 {
				scores = []int{20, 20}
				verdict = agentruntime.EvaluationFail
			}
			rows[0].Verdict = verdict
			rows[0].Judge = &agentruntime.EvaluationJudgeEvidence{RequestStyle: "openai-chat-completions-v1", Model: "judge-model", ModelVersion: "v1", Provenance: "user-declared", EvaluatorDigest: agentruntime.EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: agentruntime.EvaluationJudgeConfigurationDigest("https://private-endpoint-canary.invalid/v1", "judge-model", "v1"), MinScore: 70, Scores: scores, ModelMatches: []string{"requested", "version-qualified"}}
		}
		observations = append(observations, suitePassingObservation{CaseDigest: frame.Metadata.CaseDigest, InputDigest: frame.Metadata.InputDigest, EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: frame.Case.ID, TaskName: fmt.Sprintf("task-%d", i), TaskUID: fmt.Sprintf("uid-%d", i), Verdict: verdict, AnswerSHA256: agentruntime.EvaluationAnswerDigest("4"), Assertions: rows}})
	}
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), prepared.Path, suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	metadata := publicMetadataFixture()
	e, expected, err := suitePublicEvidence(t.Context(), artifact, suite, receipt, metadata)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agentsuite.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentsuite.VerifyEvaluation(prepared.Path, expected, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if len(e.Results) != 2 || len(e.Results[0].Runs) != 3 || e.Evaluation.Cases[0].Runs != 3 || e.Evaluation.Cases[0].MinPasses != 2 || e.Results[0].Runs[0].Outcome != "fail" || e.Results[0].Outcome != "pass" {
		t.Fatal("countable failed run lost", e.Results)
	}
	j := e.Results[0].Runs[0].Assertions[0].Judge
	if j.ObservedModels[0] != "judge-model" || j.ObservedModels[1] != "judge-model-v1" || j.Scores[0] != 20 || j.MinScore != 70 {
		t.Fatal("judge identity/outcomes changed", j)
	}
	// Independent literal JCS schema: no rubric, endpoint or private config hash.
	projected := fmt.Sprintf(`{"domain":"kmx.public-judge-config/v1","maxCompletionTokens":1024,"minScore":70,"model":"judge-model","policy":"%s","requestStyle":"openai-chat-completions-v1","samples":2,"sampling":"no-override","stream":false,"version":"v1"}`, agentruntime.EvaluationJudgeEvaluatorDigest())
	if j.Configuration.Policy.ConfigurationDigest != suitePublicHash([]byte(projected)) {
		t.Fatal("wrong public judge projection")
	}
	for _, canary := range []string{suiteJudgeCanary, "private-endpoint-canary", receipt.Cases[0].InputDigest, receipt.Cases[0].AnswerDigest, receipt.Cases[0].Assertions[0].DefinitionDigest, receipt.Cases[0].Assertions[0].Judge.ConfigurationDigest} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("private projection leaked", canary)
		}
	}
	metadata.BinaryDigest = "sha256:" + strings.Repeat("f", 64)
	second, _, err := suitePublicEvidence(t.Context(), artifact, suite, receipt, metadata)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := agentsuite.Marshal(second)
	if !bytes.Equal(raw, again) {
		t.Fatal("optional PRIVATE binary digest affected public identity")
	}
	// Independent expectations must reject a changed claim, not bless it.
	e.Execution.Runtime.Digest = "sha256:" + strings.Repeat("f", 64)
	changed, _ := agentsuite.Marshal(e)
	if _, err := agentsuite.VerifyEvaluation(prepared.Path, expected, bytes.NewReader(changed)); err == nil {
		t.Fatal("substituted execution accepted")
	}
	receipt.Cases[0].Assertions[0].Judge.ModelMatches[0] = "prefix-match"
	if _, _, err := suitePublicEvidence(t.Context(), artifact, suite, receipt, metadata); err == nil {
		t.Fatal("lossy judge identity reconstructed")
	}
}

func TestSuitePublicKagentVerifiedProjection(t *testing.T) {
	receipts := suiteDiffFixtures(t)
	opt := suiteKagentEvaluationOptions(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.prepared.Path, p.suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	r := receipts["native-kagent"]
	e, expected, err := suitePublicEvidence(t.Context(), artifact, p.suite, r, publicMetadataFixture())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := agentsuite.Marshal(e)
	if _, err := agentsuite.VerifyEvaluation(p.prepared.Path, expected, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if e.Execution.Renderer.ID != "kmx.native-kagent" || e.Execution.Renderer.Version != "v2" || e.Execution.Profile.ID != "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai" || e.Execution.Profile.Version != "v1" {
		t.Fatal("kagent labels", e.Execution)
	}
	before := e.Execution
	// Both the index and amd64 platform pins are qualified equivalents.
	r.Kagent.Observation.Images[0].Digest = "7f788d09d5f21ccc3068679310943dcf4f0df19e1a93960423810438f4238b24"
	r.Kagent.Observation.Subject.Runtime.ControllerImages[0].Digest = "8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8"
	runtime, harness, err := suitePublicRuntime(r)
	if err != nil || runtime != before.Runtime || harness != before.Harness {
		t.Fatal("observed spelling changed identity", runtime, harness, err)
	}
	for _, canary := range []string{r.Kagent.Observation.Subject.BindingDigest, r.Kagent.Observation.Subject.PayloadDigest, r.Kagent.Observation.Subject.RenderedDigest, r.Kagent.Observation.Subject.Secret.Name} {
		if strings.Contains(string(raw), canary) {
			t.Fatal("private kagent binding leaked", canary)
		}
	}
}

func TestSuitePublicUnsupportedModelRefusesWithoutNormalization(t *testing.T) {
	for _, model := range []string{"organization/model", "model:tag", "model+variant", "private.host.invalid", strings.Repeat("m", 129)} {
		t.Run(model[:min(len(model), 20)], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			instructions := filepath.Join(t.TempDir(), "instructions.md")
			writeFile(t, instructions, "private-prompt-canary")
			if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Model: model, InstructionsFile: instructions, Runtime: workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}}); err != nil {
				t.Fatal(err)
			}
			prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			suite, err := workspace.Load(t.Context(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), prepared.Path, suite.Digest)
			if err != nil {
				t.Fatal(err)
			}
			host := suiteSessionsHostObservation{Mode: "operator-observed-development", HostID: "host", JournalID: "journal", RuntimeVersion: workspace.AgentSessionsVersion, RuntimeBinaryDigest: "sha256:" + strings.Repeat("a", 64), RuntimeConfigDigest: "sha256:" + strings.Repeat("b", 64), InferenceID: "inference", InferenceRevision: "sha256:" + strings.Repeat("c", 64), Model: model, Harness: "chat", HarnessVersion: "1", Retention: "operator-managed"}
			subject, err := newSuiteSessionsSubject(suite, host)
			if err != nil {
				t.Fatal(err)
			}
			r := &suitePassingReceipt{ExecutionMode: "native-agentsessions", Sessions: &suiteSessionsPassingProof{Subject: subject, RequiredImages: []string{}}}
			if _, err := suitePublicExpected(artifact, suite, r, publicMetadataFixture()); err != errSuitePublicProjection {
				t.Fatal("unsupported model accepted or unsafe error", err)
			}
		})
	}
}
