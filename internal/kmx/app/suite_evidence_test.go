package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// Independent framing/hash expectations, not the evidence validator/builders.
func suiteTestHash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func suiteTestFrame(name string, raw []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %d\n", name, len(raw))
	b.Write(raw)
	b.WriteByte('\n')
	return b.Bytes()
}
func suiteTestSubject(t *testing.T, s *workspace.NativeSuite, subject suiteNativeSubject) suiteNativeSubject {
	t.Helper()
	subject.Suite = s.Name
	subject.SuiteDigest = s.Digest
	subject.GraphDigest = s.GraphDigest
	subject.Member = s.MemberID
	subject.MemberDigest = s.MemberDigest
	subject.InstructionsDigest = s.InstructionsDigest
	subject.Model = s.Model
	subject.CompositionDigest = s.CompositionDigest
	subject.BuildProfileDigest = s.BuildProfileDigest
	subject.Renderer = s.RendererVersion
	subject.RequiredImages = []string{}
	bind, _ := spec.Marshal(struct {
		Model          string             `json:"model"`
		RequiredImages []string           `json:"requiredImages"`
		Binding        suiteNativeBinding `json:"binding"`
		Runtime        suiteNativeRuntime `json:"runtime"`
	}{subject.Model, []string{}, subject.Binding, subject.Runtime})
	// RenderedBundleDigest frames documents using their zero-based ordinal.
	subject.BindingDigest = suiteTestHash(append(suiteTestFrame("rendered/000.yaml", []byte("kmx.native-orka.binding/v1")), suiteTestFrame("rendered/001.yaml", bind)...))
	b, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: s.MemberID, Namespace: subject.Target.Namespace, Description: s.Description, Model: s.Model, Instructions: string(s.Instructions), ProviderType: "openai", BaseURL: subject.Binding.Provider.BaseURL, SecretName: subject.Binding.Provider.SecretName, SecretKey: subject.Binding.Provider.SecretKey})
	if err != nil {
		t.Fatal(err)
	}
	var frames []byte
	for i, doc := range b.Documents() {
		raw, _ := yaml.Marshal(doc)
		frames = append(frames, suiteTestFrame(fmt.Sprintf("rendered/%03d.yaml", i), raw)...)
	}
	subject.PayloadDigest = suiteTestHash(frames)
	subject.RenderedDigest = ""
	raw, _ := spec.Marshal(subject)
	subject.RenderedDigest = suiteTestHash(append(suiteTestFrame("rendered/000.yaml", []byte("kmx.native-orka.revision/v1")), suiteTestFrame("rendered/001.yaml", raw)...))
	return subject
}
func suiteEvidenceFixture(t *testing.T) (*workspace.NativeSuite, suiteNativeSubject, suiteEvaluationReceipt, suitePromotionPolicy) {
	return suiteEvidenceFixtureCases(t, map[string]string{"alpha.yaml": "id: first\ninput: INPUT_PAYLOAD_CANARY\nassertions:\n  - id: check\n    type: contains\n    value: OPERAND_PAYLOAD_CANARY\n", "beta.yaml": "id: second\ninput: OTHER_INPUT_CANARY\nexpectContains: [OTHER_OPERAND_CANARY]\n"})
}
func suiteEvidenceFixtureCases(t *testing.T, authored map[string]string) (*workspace.NativeSuite, suiteNativeSubject, suiteEvaluationReceipt, suitePromotionPolicy) {
	t.Helper()
	root := t.TempDir()
	inst := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(inst, []byte("INSTRUCTION_PAYLOAD_CANARY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Model: "test-model", InstructionsFile: inst}); err != nil {
		t.Fatal(err)
	}
	// Replace the authored inventory with two cases. IDs are not filenames.
	entries, _ := os.ReadDir(filepath.Join(root, "eval"))
	for _, e := range entries {
		os.Remove(filepath.Join(root, "eval", e.Name()))
	}
	for name, raw := range authored {
		if err := os.WriteFile(filepath.Join(root, "eval", name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	sub := suiteTestSubject(t, s, suiteNativeSubject{Target: suiteNativeTarget{Context: "proof", ClusterUID: "cluster-proof", Namespace: "orka-system", NamespaceUID: "namespace-proof"}, Binding: suiteNativeBinding{Provider: suiteNativeProvider{Name: "inference", Namespace: "orka-system", UID: "provider-proof", Generation: 1, ResourceVersion: "9", Type: "openai", BaseURL: "https://model.example.invalid/v1", SecretName: "model-secret", SecretKey: "api-key"}, Secret: suiteNativeSecret{Name: "model-secret", Namespace: "orka-system", UID: "secret-proof", ResourceVersion: "7", Key: "api-key", KeyPresent: true}}, Runtime: suiteNativeRuntime{ID: "orka", Version: OrkaVersion, Namespace: OrkaNamespace, ControllerName: "orka-controller", ControllerUID: "controller-proof", ControllerGeneration: 1, ControllerServiceAccount: "orka-controller", ControllerImage: "ghcr.io/orka-agents/orka@" + orkaControllerDigest, Mode: "harness-v2", WatchNamespace: "orka-system", Helm: suiteNativeHelmIdentity{Chart: "orka-0.2.0", Instance: "orka", App: "orka", ManagedBy: "Helm"}, AIWorkerImage: "ghcr.io/orka-agents/orka/ai-worker@" + orkaAIWorkerDigest, WorkerAccount: "orka-ai-worker", WorkerAccountUID: "worker-proof", WorkerTrust: "ai"}})
	owner := orkaSuiteOwnership{Suite: sub.Suite, Digest: sub.SuiteDigest, Member: sub.Member, Renderer: sub.Renderer, BindingDigest: sub.BindingDigest, RenderedDigest: sub.RenderedDigest}
	r := suiteEvaluationReceipt{SchemaVersion: 1, Domain: "suite-evaluation", ExecutionMode: "native-orka", RunID: strings.Repeat("a", 32), Subject: sub, Placement: suiteEvidencePlacement{Complete: true, Ownership: owner, Before: []suiteNativePairObservation{}, Resources: []suiteNativeResource{}}, FullCaseSet: true, PromotionEligible: true, Result: rt.EvaluationPass, Cases: []suiteEvidenceCase{}}
	for i, kind := range []string{"Provider", "Agent"} {
		r.Placement.Before = append(r.Placement.Before, suiteNativePairObservation{Kind: kind, Name: s.MemberID, Namespace: sub.Target.Namespace})
		r.Placement.Resources = append(r.Placement.Resources, suiteNativeResource{Kind: kind, Name: s.MemberID, Namespace: sub.Target.Namespace, UID: kind + "-proof", Generation: 1, SpecDigest: strings.Repeat(string(rune('c'+i)), 64), Outcome: rt.ResourceCreated, AttemptedWrite: true, Applied: true, Ready: true})
	}
	var caseFrames []byte
	for i, p := range s.Cases {
		c, err := rt.ParseEvaluationCase(p.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		rows := []suiteEvidenceAssertion{}
		for _, a := range rt.EvaluationCaseAssertions(c) {
			operand := a.Value
			switch a.Type {
			case "regex":
				operand = a.Pattern
			case "toolCalled", "toolNotCalled":
				operand = a.Tool
			case "llmJudge":
				operand = a.Rubric
			}
			definition := append(suiteTestFrame("type", []byte(a.Type)), suiteTestFrame("operand", []byte(operand))...)
			row := suiteEvidenceAssertion{ID: a.ID, Type: a.Type, EvaluatorVersion: "kmx.assertions/v1", Verdict: rt.EvaluationPass, Reason: "assertion satisfied"}
			if a.Type == "llmJudge" {
				evaluator := suiteTestHash([]byte(rt.EvaluationJudgePolicy))
				for _, entry := range []struct{ name, value string }{{"minScore", fmt.Sprint(*a.MinScore)}, {"judgeModel", a.JudgeModel}, {"judgeVersion", a.JudgeVersion}, {"evaluatorDigest", evaluator}} {
					definition = append(definition, suiteTestFrame(entry.name, []byte(entry.value))...)
				}
				row.EvaluatorVersion = "kmx.llm-judge/v1"
				config := append(suiteTestFrame("endpoint", []byte("https://judge.example.invalid/v1")), suiteTestFrame("model", []byte(a.JudgeModel))...)
				config = append(config, suiteTestFrame("modelVersion", []byte(a.JudgeVersion))...)
				config = append(config, suiteTestFrame("evaluatorDigest", []byte(evaluator))...)
				row.Judge = &rt.EvaluationJudgeEvidence{Model: a.JudgeModel, ModelVersion: a.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: evaluator, ConfigurationDigest: suiteTestHash(config), MinScore: *a.MinScore, Scores: []int{90, 90}}
			}
			if a.Type == "toolCalled" || a.Type == "toolNotCalled" {
				row.Verdict = rt.EvaluationUnknown
				row.Reason = "runtime does not support tools"
			}
			row.DefinitionDigest = suiteTestHash(definition)
			rows = append(rows, row)
		}
		r.Cases = append(r.Cases, suiteEvidenceCase{ID: c.ID, Path: p.Path, CaseDigest: suiteTestHash(p.Bytes), InputDigest: suiteTestHash([]byte(c.Input)), TaskName: "eval-task-" + string(rune('a'+i)), TaskUID: "task-uid-" + string(rune('a'+i)), AnswerSHA256: suiteTestHash([]byte("ANSWER_PAYLOAD_CANARY")), Verdict: rt.EvaluationPass, Assertions: rows})
		caseFrames = append(caseFrames, suiteTestFrame("eval/"+strings.TrimPrefix(p.Path, "metadata/kmx/eval/"), p.Bytes)...)
	}
	r.CasesDigest = suiteTestHash(caseFrames)
	raw, _ := spec.Marshal(r)
	policy := suitePromotionPolicy{SchemaVersion: 1, Domain: "suite-promotion", ReceiptDigest: "sha256:" + suiteTestHash(raw), RunID: r.RunID, Evaluated: sub, Destination: sub, AllowedTransitionPaths: []string{}}
	return s, sub, r, policy
}
func suiteCloneReceipt(t *testing.T, r suiteEvaluationReceipt) suiteEvaluationReceipt {
	t.Helper()
	raw, _ := json.Marshal(r)
	var out suiteEvaluationReceipt
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestSuiteEvidenceFullReceiptRoundTrip(t *testing.T) {
	s, sub, r, p := suiteEvidenceFixture(t)
	raw, _ := json.MarshalIndent(r, "", "  ")
	parsed, err := decodeSuiteEvaluationReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := canonicalSuiteEvaluationReceipt(s, sub, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if digest != p.ReceiptDigest || !bytes.Equal(canonical, mustSuiteCanonical(t, r)) {
		t.Fatal("canonical receipt digest differs from independent SHA-256")
	}
	if _, err := gateSuitePromotion(s, sub, sub, parsed, p); err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"INSTRUCTION_PAYLOAD_CANARY", "INPUT_PAYLOAD_CANARY", "OPERAND_PAYLOAD_CANARY", "ANSWER_PAYLOAD_CANARY", "OTHER_INPUT_CANARY", "OTHER_OPERAND_CANARY"} {
		if bytes.Contains(canonical, []byte(canary)) {
			t.Fatalf("payload serialized: %s", canary)
		}
	}
}
func mustSuiteCanonical(t *testing.T, v any) []byte {
	t.Helper()
	b, e := spec.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestSuiteEvidenceRejectsMetadataAndVerdictDiscrepancies(t *testing.T) {
	s, sub, base, _ := suiteEvidenceFixture(t)
	changes := map[string]func(*suiteEvaluationReceipt){
		"version": func(r *suiteEvaluationReceipt) { r.SchemaVersion = 2 }, "domain": func(r *suiteEvaluationReceipt) { r.Domain = "bundle" }, "preview": func(r *suiteEvaluationReceipt) { r.ExecutionMode = "preview" }, "run-id": func(r *suiteEvaluationReceipt) { r.RunID = "latest" },
		"suite-not-graph": func(r *suiteEvaluationReceipt) { r.Subject.SuiteDigest = r.Subject.GraphDigest }, "model": func(r *suiteEvaluationReceipt) { r.Subject.Model = "other" }, "member": func(r *suiteEvaluationReceipt) { r.Subject.Member = "other" }, "profile": func(r *suiteEvaluationReceipt) { r.Subject.BuildProfileDigest = "sha256:" + strings.Repeat("1", 64) }, "renderer": func(r *suiteEvaluationReceipt) { r.Subject.Renderer = "future" }, "images-null": func(r *suiteEvaluationReceipt) { r.Subject.RequiredImages = nil }, "images-required": func(r *suiteEvaluationReceipt) { r.Subject.RequiredImages = []string{"image"} },
		"binding": func(r *suiteEvaluationReceipt) { r.Subject.Binding.Secret.ResourceVersion = "8" }, "target": func(r *suiteEvaluationReceipt) { r.Subject.Target.ClusterUID = "other" }, "runtime": func(r *suiteEvaluationReceipt) { r.Subject.Runtime.WorkerAccountUID = "other" }, "binding-digest": func(r *suiteEvaluationReceipt) { r.Subject.BindingDigest = strings.Repeat("e", 64) }, "render-digest": func(r *suiteEvaluationReceipt) { r.Subject.RenderedDigest = strings.Repeat("e", 64) }, "payload-digest": func(r *suiteEvaluationReceipt) { r.Subject.PayloadDigest = strings.Repeat("e", 64) },
		"case-set": func(r *suiteEvaluationReceipt) { r.CasesDigest = strings.Repeat("f", 64) }, "missing-case": func(r *suiteEvaluationReceipt) { r.Cases = r.Cases[:1] }, "duplicate-case": func(r *suiteEvaluationReceipt) { r.Cases[1] = r.Cases[0] }, "extra-case": func(r *suiteEvaluationReceipt) { r.Cases = append(r.Cases, r.Cases[0]) }, "case-path": func(r *suiteEvaluationReceipt) { r.Cases[0].Path = "metadata/kmx/eval/wrong.yaml" }, "case-id": func(r *suiteEvaluationReceipt) { r.Cases[0].ID = "wrong" }, "case-digest": func(r *suiteEvaluationReceipt) { r.Cases[0].CaseDigest = strings.Repeat("a", 64) }, "input-digest": func(r *suiteEvaluationReceipt) { r.Cases[0].InputDigest = strings.Repeat("a", 64) }, "assertion-id": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].ID = "other" }, "assertion-digest": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].DefinitionDigest = strings.Repeat("a", 64) }, "assertion-kind": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "regex" }, "evaluator-version": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].EvaluatorVersion = "future" }, "missing-assertion": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions = nil }, "duplicate-assertion": func(r *suiteEvaluationReceipt) {
			r.Cases[0].Assertions = append(r.Cases[0].Assertions, r.Cases[0].Assertions[0])
		},
		"pass-without-task": func(r *suiteEvaluationReceipt) { r.Cases[0].TaskUID = "" }, "pass-without-answer": func(r *suiteEvaluationReceipt) { r.Cases[0].AnswerSHA256 = "" }, "duplicate-task": func(r *suiteEvaluationReceipt) { r.Cases[1].TaskUID = r.Cases[0].TaskUID }, "bad-answer": func(r *suiteEvaluationReceipt) { r.Cases[0].AnswerSHA256 = "sha256:" + r.Cases[0].AnswerSHA256 }, "mixed-verdict": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].Verdict = rt.EvaluationFail }, "false-overall": func(r *suiteEvaluationReceipt) { r.Result = rt.EvaluationFail }, "raw-detail": func(r *suiteEvaluationReceipt) { r.Cases[0].Detail = "PRIVATE_PAYLOAD" }, "raw-reason": func(r *suiteEvaluationReceipt) { r.Cases[0].Assertions[0].Reason = "PRIVATE_PAYLOAD" }, "diagnostic-promotion": func(r *suiteEvaluationReceipt) { r.FullCaseSet = false }, "incomplete-promotion": func(r *suiteEvaluationReceipt) { r.Placement.Complete = false }, "residual-promotion": func(r *suiteEvaluationReceipt) { r.Placement.Resources[0].Ambiguous = true }, "unapplied": func(r *suiteEvaluationReceipt) { r.Placement.Resources[0].Applied = false }, "not-ready": func(r *suiteEvaluationReceipt) { r.Placement.Resources[0].Ready = false }, "missing-owned-uid": func(r *suiteEvaluationReceipt) { r.Placement.Resources[0].UID = "" }, "missing-generation": func(r *suiteEvaluationReceipt) { r.Placement.Resources[0].Generation = 0 }, "owner-marker": func(r *suiteEvaluationReceipt) { r.Placement.Ownership.Digest = r.Subject.GraphDigest },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := suiteCloneReceipt(t, base)
			change(&r)
			if err := validateSuiteEvaluationReceipt(s, sub, r); err == nil {
				t.Fatal("discrepancy accepted")
			}
		})
	}
}

// A Ready source Provider may omit its endpoint. Exercise the actual native
// prepare/place path, not a subject fabricated by the pure evidence fixtures.
func TestSuiteEvidenceNativeProviderDefaultEndpoint(t *testing.T) {
	f := newSuiteNativeFixture(t)
	delete(nativeSpec(f.source), "baseURL")
	plan := f.prepare(t)
	if plan.Subject.Binding.Provider.BaseURL != "" {
		t.Fatal("native preparation invented a Provider endpoint")
	}
	if _, present := nativeSpec(plan.bundle.Provider)["baseURL"]; present {
		t.Fatal("native renderer did not retain the default endpoint")
	}
	unknown := make([]suiteEvidenceCase, 0, len(f.suite.Cases))
	for _, packaged := range f.suite.Cases {
		row, err := unknownSuiteEvidenceCase(packaged, "")
		if err != nil {
			t.Fatal(err)
		}
		unknown = append(unknown, row)
	}
	t.Run("unknown-history", func(t *testing.T) {
		partial := &suiteNativePlacement{Subject: plan.Subject, Before: plan.Before, Resources: []suiteNativeResource{}}
		receipt, err := newSuiteEvaluationReceipt(f.suite, partial, strings.Repeat("a", 32), false, unknown, "placement unavailable")
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Result != rt.EvaluationUnknown || receipt.PromotionEligible || len(receipt.Placement.Resources) != 0 || receipt.Subject.Binding.Provider.BaseURL != "" {
			t.Fatal("default-endpoint unknown history invented placement or changed binding")
		}
		workspace, state, out := suiteOutputFixture(t, "unknown.json")
		output, err := preflightSuiteReceiptOutput(workspace, state, out)
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		saved, err := publishSuiteEvaluationReceipt(f.suite, plan.Subject, receipt, output)
		if err != nil {
			t.Fatal(err)
		}
		if !saved.HistoryPublished || !saved.ExportPublished {
			t.Fatal("honest unknown history not saved")
		}
		parsed, err := readSuiteEvaluationReceiptFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSuiteEvaluationReceipt(f.suite, plan.Subject, parsed); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("complete-receipt-gate", func(t *testing.T) {
		placement, err := f.app.placeSuiteNative(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.app.verifySuiteNative(t.Context(), placement); err != nil {
			t.Fatal(err)
		}
		if !placement.Complete || len(placement.Resources) != 2 || !placement.Resources[0].Ready || !placement.Resources[1].Ready {
			t.Fatal("native default-endpoint placement not complete")
		}
		// Supplied execution metadata exercises Task5's pure boundary. No Task or
		// model execution claim is made by this prepare/place/evidence regression.
		results := make([]suiteEvidenceCase, 0, len(f.suite.Cases))
		for _, packaged := range f.suite.Cases {
			c, err := rt.ParseEvaluationCase(packaged.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if c.ID != "example" {
				t.Fatal("unexpected authored case fixture")
			}
			native := suiteNativeCaseResult{CaseDigest: suiteTestHash(packaged.Bytes), InputDigest: suiteTestHash([]byte(c.Input)), EvaluationReceipt: rt.EvaluationReceipt{CaseID: c.ID, Verdict: rt.EvaluationPass, TaskName: "eval-default-endpoint", TaskUID: "default-endpoint-task-uid", AnswerSHA256: suiteTestHash([]byte("4")), Assertions: rt.EvaluateAssertions(c, rt.EvaluationEvidence{Answer: "4", AnswerAvailable: true})}}
			mapped, err := suiteEvidenceCaseFromNative(packaged, native)
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, mapped)
		}
		receipt, err := newSuiteEvaluationReceipt(f.suite, placement, strings.Repeat("b", 32), false, results, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSuiteEvaluationReceipt(f.suite, plan.Subject, receipt); err != nil {
			t.Fatal(err)
		}
		raw, digest, err := canonicalSuiteEvaluationReceipt(f.suite, plan.Subject, receipt)
		if err != nil {
			t.Fatal(err)
		}
		if digest != "sha256:"+suiteTestHash(raw) || !bytes.Contains(raw, []byte(`"baseURL":""`)) {
			t.Fatal("default endpoint omitted from canonical evidence or digest changed")
		}
		parsed, err := decodeSuiteEvaluationReceipt(raw)
		if err != nil {
			t.Fatal(err)
		}
		policy := suitePromotionPolicy{SchemaVersion: 1, Domain: "suite-promotion", ReceiptDigest: digest, RunID: receipt.RunID, Evaluated: plan.Subject, Destination: plan.Subject, AllowedTransitionPaths: []string{}}
		parsedPolicy, err := decodeSuitePromotionPolicy(mustSuiteCanonical(t, policy))
		if err != nil {
			t.Fatal(err)
		}
		decision, err := gateSuitePromotion(f.suite, plan.Subject, plan.Subject, parsed, parsedPolicy)
		if err != nil {
			t.Fatal(err)
		}
		if decision.DestinationEvaluated || !receipt.PromotionEligible {
			t.Fatal("default-endpoint permission lost or claimed destination execution")
		}
		for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"baseURL":"",`), nil, 1), bytes.Replace(raw, []byte(`"baseURL":""`), []byte(`"baseURL":null`), 1)} {
			if _, err := decodeSuiteEvaluationReceipt(bad); err == nil {
				t.Fatal("baseURL JSON presence/null requirement relaxed")
			}
		}
		if _, err := decodeSuitePromotionPolicy(bytes.Replace(mustSuiteCanonical(t, policy), []byte(`"baseURL":"",`), nil, 1)); err == nil {
			t.Fatal("policy baseURL member became optional")
		}
	})
}
func TestSuiteEvidenceOnlyProviderBaseURLMayBeEmpty(t *testing.T) {
	_, sub, _, _ := suiteEvidenceFixture(t)
	sub.Binding.Provider.BaseURL = ""
	if !suiteMetadataStrings(sub.Binding) {
		t.Fatal("valid default endpoint rejected")
	}
	for _, badURL := range []string{" ", "\t", "https://model.example.invalid/*", "https://model.example.invalid/?query=1", "sk-" + strings.Repeat("a", 48)} {
		bad := sub.Binding
		bad.Provider.BaseURL = badURL
		if suiteMetadataStrings(bad) {
			t.Fatal("unsafe or whitespace endpoint accepted")
		}
	}
	mutations := map[string]func(*suiteNativeBinding){
		"provider-name": func(b *suiteNativeBinding) { b.Provider.Name = "" }, "provider-namespace": func(b *suiteNativeBinding) { b.Provider.Namespace = "" }, "provider-uid": func(b *suiteNativeBinding) { b.Provider.UID = "" }, "provider-version": func(b *suiteNativeBinding) { b.Provider.ResourceVersion = "" }, "provider-type": func(b *suiteNativeBinding) { b.Provider.Type = "" }, "provider-secret-name": func(b *suiteNativeBinding) { b.Provider.SecretName = "" }, "provider-secret-key": func(b *suiteNativeBinding) { b.Provider.SecretKey = "" },
		"secret-name": func(b *suiteNativeBinding) { b.Secret.Name = "" }, "secret-namespace": func(b *suiteNativeBinding) { b.Secret.Namespace = "" }, "secret-uid": func(b *suiteNativeBinding) { b.Secret.UID = "" }, "secret-version": func(b *suiteNativeBinding) { b.Secret.ResourceVersion = "" }, "secret-key": func(b *suiteNativeBinding) { b.Secret.Key = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := sub.Binding
			mutate(&bad)
			if suiteMetadataStrings(bad) {
				t.Fatal("required binding identity became optional")
			}
		})
	}
}
func TestSuiteEvidenceNativeMappingAndDiagnosticHistory(t *testing.T) {
	s, sub, base, _ := suiteEvidenceFixture(t)
	initial, err := unknownSuiteEvidenceCase(s.Cases[0], "")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Verdict != rt.EvaluationUnknown || initial.TaskUID != "" || initial.AnswerSHA256 != "" || initial.Detail != evaluationExecutionUnavailable {
		t.Fatal("unrun case invented native result")
	}
	native := suiteNativeCaseResult{CaseDigest: base.Cases[0].CaseDigest, InputDigest: base.Cases[0].InputDigest, EvaluationReceipt: rt.EvaluationReceipt{CaseID: base.Cases[0].ID, TaskName: base.Cases[0].TaskName, TaskUID: base.Cases[0].TaskUID, AnswerSHA256: base.Cases[0].AnswerSHA256, Verdict: rt.EvaluationPass, Matched: []string{"OPERAND_PAYLOAD_CANARY"}, Missing: []string{"PRIVATE_MISSING_CANARY"}}}
	for _, a := range base.Cases[0].Assertions {
		native.Assertions = append(native.Assertions, rt.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: a.DefinitionDigest, Verdict: a.Verdict, Reason: a.Reason, Judge: a.Judge})
	}
	mapped, err := suiteEvidenceCaseFromNative(s.Cases[0], native)
	if err != nil {
		t.Fatal(err)
	}
	placement := &suiteNativePlacement{Subject: sub, Before: base.Placement.Before, Resources: base.Placement.Resources, Complete: true}
	r, err := newSuiteEvaluationReceipt(s, placement, base.RunID, true, []suiteEvidenceCase{mapped}, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.PromotionEligible || r.FullCaseSet || r.Result != rt.EvaluationPass || len(r.Cases) != 1 {
		t.Fatal("diagnostic selected run qualified")
	}
	raw, _, err := canonicalSuiteEvaluationReceipt(s, sub, r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("OPERAND_PAYLOAD_CANARY")) || bytes.Contains(raw, []byte("PRIVATE_MISSING_CANARY")) {
		t.Fatal("legacy operands persisted")
	}
	// Constructor must own its snapshot; mutable execution arrays are not history.
	placement.Resources[0].UID = "mutated-after-build"
	mapped.Assertions[0].Reason = "PRIVATE_MUTATION"
	if r.Placement.Resources[0].UID == "mutated-after-build" || r.Cases[0].Assertions[0].Reason == "PRIVATE_MUTATION" {
		t.Fatal("receipt aliases mutable execution")
	}
	native.InputDigest = strings.Repeat("f", 64)
	if _, err := suiteEvidenceCaseFromNative(s.Cases[0], native); err == nil {
		t.Fatal("native packaged identity mismatch accepted")
	}
}
func TestSuiteEvidenceJudgeAndEveryAssertionVersionRule(t *testing.T) {
	rawCase := "id: all-kinds\ninput: INPUT_PAYLOAD_CANARY\nassertions:\n  - {id: contains, type: contains, value: operand}\n  - {id: absent, type: notContains, value: absent}\n  - {id: regex, type: regex, pattern: '^ok$'}\n  - {id: called, type: toolCalled, tool: search}\n  - {id: notcalled, type: toolNotCalled, tool: search}\n  - {id: judge, type: llmJudge, rubric: RUBRIC_PAYLOAD_CANARY, minScore: 75, judgeModel: judge-model, judgeVersion: v1}\n"
	s, sub, r, _ := suiteEvidenceFixtureCases(t, map[string]string{"all.yaml": rawCase})
	r.Cases[0].Verdict = rt.EvaluationUnknown
	r.Result = rt.EvaluationUnknown
	r.PromotionEligible = false
	if err := validateSuiteEvaluationReceipt(s, sub, r); err != nil {
		t.Fatal(err)
	}
	for i := range r.Cases[0].Assertions {
		t.Run(r.Cases[0].Assertions[i].Type, func(t *testing.T) {
			bad := suiteCloneReceipt(t, r)
			bad.Cases[0].Assertions[i].EvaluatorVersion = "future"
			if err := validateSuiteEvaluationReceipt(s, sub, bad); err == nil {
				t.Fatal("incompatible assertion evaluator accepted")
			}
		})
	}
	judgeIndex := len(r.Cases[0].Assertions) - 1
	for _, field := range []string{"model", "version", "provenance", "evaluator", "configuration", "threshold", "scores", "missing"} {
		t.Run(field, func(t *testing.T) {
			bad := suiteCloneReceipt(t, r)
			a := &bad.Cases[0].Assertions[judgeIndex]
			switch field {
			case "model":
				a.Judge.Model = "other"
			case "version":
				a.Judge.ModelVersion = "v2"
			case "provenance":
				a.Judge.Provenance = "attested"
			case "evaluator":
				a.Judge.EvaluatorDigest = strings.Repeat("e", 64)
			case "configuration":
				a.Judge.ConfigurationDigest = "wrong"
			case "threshold":
				a.Judge.MinScore = 100
			case "scores":
				a.Judge.Scores = []int{100}
			case "missing":
				a.Judge = nil
			}
			if err := validateSuiteEvaluationReceipt(s, sub, bad); err == nil {
				t.Fatal("judge incompatibility accepted")
			}
		})
	}
	if _, err := unknownSuiteEvidenceCase(s.Cases[0], ""); err == nil {
		t.Fatal("missing judge configuration invented")
	}
	initial, err := unknownSuiteEvidenceCase(s.Cases[0], "https://judge.example.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Assertions[judgeIndex].Judge.Model != "judge-model" || len(initial.Assertions[judgeIndex].Judge.Scores) != 0 {
		t.Fatal("unknown judge lost authored identity or invented scores")
	}
	if bytes.Contains(mustSuiteCanonical(t, r), []byte("RUBRIC_PAYLOAD_CANARY")) {
		t.Fatal("rubric persisted")
	}
}
func TestSuiteEvidenceUnknownJudgesBindEachAssertionConfiguration(t *testing.T) {
	s, _, _, _ := suiteEvidenceFixtureCases(t, map[string]string{"judges.yaml": "id: judges\ninput: safe\nassertions:\n  - {id: one, type: llmJudge, rubric: first, minScore: 75, judgeModel: judge-one, judgeVersion: v1}\n  - {id: two, type: llmJudge, rubric: second, minScore: 75, judgeModel: judge-two, judgeVersion: v2}\n"})
	initial, err := unknownSuiteEvidenceCase(s.Cases[0], "https://JUDGE.example.invalid:443/")
	if err != nil {
		t.Fatal(err)
	}
	first, second := initial.Assertions[0].Judge, initial.Assertions[1].Judge
	for _, judge := range []*rt.EvaluationJudgeEvidence{first, second} {
		frame := append(suiteTestFrame("endpoint", []byte("https://judge.example.invalid/v1")), suiteTestFrame("model", []byte(judge.Model))...)
		frame = append(frame, suiteTestFrame("modelVersion", []byte(judge.ModelVersion))...)
		frame = append(frame, suiteTestFrame("evaluatorDigest", []byte(suiteTestHash([]byte(rt.EvaluationJudgePolicy))))...)
		if judge.ConfigurationDigest != suiteTestHash(frame) || len(judge.Scores) != 0 {
			t.Fatal("unknown assertion configuration not derived from its exact model/version and normalized endpoint")
		}
	}
	if first.ConfigurationDigest == second.ConfigurationDigest {
		t.Fatal("different authored judge identities shared configuration digest")
	}
}
func TestSuiteEvidenceHistoricalPlacementOutcomesMustBeCoherent(t *testing.T) {
	s, sub, base, _ := suiteEvidenceFixture(t)
	for _, mode := range []string{"duplicate-owned-uid", "reused-absent", "updated-absent", "foreign-before"} {
		t.Run(mode, func(t *testing.T) {
			r := suiteCloneReceipt(t, base)
			switch mode {
			case "duplicate-owned-uid":
				r.Placement.Resources[1].UID = r.Placement.Resources[0].UID
			case "reused-absent":
				r.Placement.Resources[0].Outcome = rt.ResourceReused
			case "updated-absent":
				r.Placement.Resources[0].Outcome = rt.ResourceUpdated
			case "foreign-before":
				b := &r.Placement.Before[0]
				b.Exists = true
				b.Identity = &suiteNativeOwnedIdentity{Kind: b.Kind, Name: b.Name, Namespace: b.Namespace, UID: r.Placement.Resources[0].UID, Generation: 1, SpecDigest: r.Placement.Resources[0].SpecDigest, Ownership: r.Placement.Ownership, Ready: true}
				b.Identity.Ownership.Suite = "other"
				r.Placement.Resources[0].Outcome = rt.ResourceReused
			}
			if err := validateSuiteEvaluationReceipt(s, sub, r); err == nil {
				t.Fatal("incoherent historical placement accepted")
			}
		})
	}
}
func TestSuiteEvidenceIncompletePlacementCannotCarryPassedResidual(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	r.Placement.Complete = false
	r.Result = rt.EvaluationUnknown
	r.PromotionEligible = false
	r.Detail = "placement unavailable"
	if err := validateSuiteEvaluationReceipt(s, sub, r); err == nil {
		t.Fatal("incomplete placement retained fabricated passing execution")
	}
}
func TestSuiteEvidenceStrictJSONPrivacy(t *testing.T) {
	_, _, r, _ := suiteEvidenceFixture(t)
	raw := string(mustSuiteCanonical(t, r))
	for name, bad := range map[string]string{"unknown": strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":1,"PRIVATE_UNKNOWN_PAYLOAD":"CANARY"`, 1), "duplicate": strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1), "case-folded": strings.Replace(raw, `"schemaVersion":1`, `"SchemaVersion":1`, 1), "null-images": strings.Replace(raw, `"requiredImages":[]`, `"requiredImages":null`, 1), "missing-images": strings.Replace(raw, `"requiredImages":[],`, "", 1), "missing-false": strings.Replace(raw, `"ambiguous":false,`, "", 1), "null-optional": strings.Replace(raw, `"assertions":[{`, `"assertions":[{"judge":null,`, 1), "trailing": raw + ` {"PRIVATE_UNKNOWN_PAYLOAD":"CANARY"}`, "oversize": strings.Repeat(" ", suiteEvidenceMaxBytes+1)} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSuiteEvaluationReceipt([]byte(bad))
			if err == nil {
				t.Fatal("unsafe JSON accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_UNKNOWN_PAYLOAD") || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("raw parser payload leaked")
			}
		})
	}
}
