package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const sessionsLiftImage = "registry.example.invalid/agentsessions/harnessnode@sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"

// sessionsLiftFixture evaluates a packaged AgentSessions workspace against the
// real pinned Sessions service and returns lift options over its layout.
func sessionsLiftFixture(t *testing.T) (ReleaseLiftOptions, *PreparedSuiteWorkspaceEvaluation, *suitePassingReceipt) {
	t.Helper()
	a, opt, _ := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	return ReleaseLiftOptions{Reference: "suite@" + p.suite.Digest, FromLayout: p.prepared.Path, Bindings: opt.Bindings, Evidence: opt.Out, HarnessImage: sessionsLiftImage}, p, r
}

func writeSessionsLiftBinding(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(privateSuiteDir(t), "lift-binding.yaml")
	if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentSessionsReleaseLiftPlanRendersFromEvidenceAndBinding(t *testing.T) {
	opt, p, r := sessionsLiftFixture(t)
	// Another environment: same model, different placement.
	opt.Bindings = writeSessionsLiftBinding(t, strings.NewReplacer("atespace: agentsuite-demo", "atespace: prod", "class: microvm, configName: microvm", "class: gvisor, configName: gvisor-default").Replace(suiteSessionsBindingYAML("fixture-model")))
	prepared, err := PrepareAgentSessionsReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.Summary()
	if s.Runtime != "agentsessions" || s.Model != "fixture-model" || s.Member != p.suite.MemberID || s.SuiteDigest != p.suite.Digest || s.Gate != "plan-only" || s.EvidenceRunID != r.RunID || s.Target != (ReleaseLiftTarget{}) || s.Inference != "" {
		t.Fatalf("summary = %+v", s)
	}
	plan := s.AgentSessions
	if plan == nil || plan.EvidenceBindingDigest != r.Sessions.Subject.BindingDigest || plan.BindingDigest == plan.EvidenceBindingDigest || s.BindingDigest != plan.BindingDigest {
		t.Fatalf("binding digests not reported: %+v", plan)
	}
	if plan.ConfigDigest != r.Sessions.Subject.ConfigDigest || plan.HarnessImage != sessionsLiftImage {
		t.Fatal("plan does not bind the evaluated chat config and image")
	}
	name := agentsessions.TemplateName(p.suite.Name, p.suite.MemberID, sessionsLiftImage, plan.BindingDigest)
	tmpl := plan.ActorTemplate
	if tmpl.Metadata != (agentsessions.ActorMetadata{Atespace: "prod", Name: name}) || tmpl.SandboxConfig.SandboxClass != "gvisor" {
		t.Fatalf("template = %+v", tmpl.Metadata)
	}
	if len(s.Resources) != 2 || s.Resources[0] != (ReleaseLiftResource{Kind: "ActorTemplate", Name: name, Action: "render", SpecDigest: plan.TemplateDigest}) ||
		s.Resources[1] != (ReleaseLiftResource{Kind: "HarnessRegistration", Name: p.suite.MemberID, Action: "render", SpecDigest: plan.RegistrationSpecDigest}) {
		t.Fatalf("resources = %+v", s.Resources)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"Answer only four.", "system_prompt", string(p.suite.Instructions)} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("plan carries instructions: %q", leaked)
		}
	}
	// The returned summary is a copy; the retained render is unchanged.
	s.AgentSessions.ActorTemplate.Containers[0].Image = "mutated"
	s.Resources[0].Name = "mutated"
	if again := prepared.Summary(); again.AgentSessions.ActorTemplate.Containers[0].Image != sessionsLiftImage || again.Resources[0].Name != name || again.PlanDigest != s.PlanDigest {
		t.Fatal("summary mutation reached the retained plan")
	}
	// Planning is deterministic for the same inputs, including through the App entry point.
	viaApp, err := (&App{}).PrepareReleaseLift(t.Context(), opt)
	if err != nil || viaApp.Summary().PlanDigest != s.PlanDigest {
		t.Fatalf("plan digest is not deterministic: %v", err)
	}
}

// Judged and repeated development evidence records the same binding digest as
// single-run evidence, and its threshold-qualified rows (including an actual
// failed run) feed the plan-only render.
func TestAgentSessionsReleaseLiftPlanFromJudgedRepeatedEvidence(t *testing.T) {
	a, opt, executions := suiteSessionsEvaluationFixture(t)
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), suiteJudgeYAML+"runs: 3\nminPasses: 2\n")
	endpoint, samples := suiteJudgeFixtureServer(t, func(_ *http.Request, n int) int {
		if n <= 2 {
			return 20
		}
		return 90
	})
	opt.JudgeBaseURL = endpoint
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cases) != 4 || r.Cases[0].Verdict != "fail" || r.Cases[0].Assertions[0].Judge == nil || r.Sessions.Subject.BindingDigest != suiteSessionsFixtureBindingDigest(t) {
		t.Fatal("judged repeated evidence lost its rows or binding digest")
	}
	prepared, err := PrepareAgentSessionsReleaseLift(t.Context(), ReleaseLiftOptions{Reference: "suite@" + p.suite.Digest, FromLayout: p.prepared.Path, Bindings: opt.Bindings, Evidence: opt.Out, HarnessImage: sessionsLiftImage})
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.Summary()
	if s.AgentSessions == nil || s.AgentSessions.EvidenceBindingDigest != suiteSessionsFixtureBindingDigest(t) || s.AgentSessions.BindingDigest != s.AgentSessions.EvidenceBindingDigest || s.EvidenceRunID != r.RunID {
		t.Fatalf("plan does not report the evaluated binding: %+v", s.AgentSessions)
	}
	if executions.Load() != 4 || samples.Load() != 6 {
		t.Fatal("lift planning executed work", executions.Load(), samples.Load())
	}
}

func TestAgentSessionsReleaseLiftRefusesModelMismatchClearly(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.Bindings = writeSessionsLiftBinding(t, suiteSessionsBindingYAML("other-model"))
	_, err := PrepareAgentSessionsReleaseLift(t.Context(), opt)
	if !errors.Is(err, ErrReleaseLift) || !errors.Is(err, agentruntime.ErrAgentSessionsModelMismatch) || !strings.Contains(err.Error(), `serves model "other-model" but the suite pins "fixture-model"`) {
		t.Fatalf("model mismatch planned or unclear: %v", err)
	}
}

// writeSessionsEvidenceV1 writes a copy of the evidence as an AgentSessions
// passing receipt v1, without agentsessions.subject.bindingDigest.
func writeSessionsEvidenceV1(t *testing.T, evidence string) string {
	t.Helper()
	raw, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["domain"] = suiteSessionsPassingDomainV1
	delete(document["agentsessions"].(map[string]any)["subject"].(map[string]any), "bindingDigest")
	old, err := suitecore.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateSuiteDir(t), "pass-v1.json")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Receipt v1 evidence names the evaluate-again remedy through the plan-only
// entry point and both common App entry point paths.
func TestAgentSessionsReleaseLiftRefusesReceiptV1(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.Evidence = writeSessionsEvidenceV1(t, opt.Evidence)
	a, _, f := nativeFixture(t)
	for name, prepare := range map[string]func() (*PreparedReleaseLift, error){
		"plan-only": func() (*PreparedReleaseLift, error) { return PrepareAgentSessionsReleaseLift(t.Context(), opt) },
		"app":       func() (*PreparedReleaseLift, error) { return a.PrepareReleaseLift(t.Context(), opt) },
		"cluster path": func() (*PreparedReleaseLift, error) {
			o := opt
			o.HarnessImage, o.ToContext = "", "kind-test"
			return a.PrepareReleaseLift(t.Context(), o)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := prepare()
			if p != nil || !errors.Is(err, ErrReleaseLift) || !errors.Is(err, errSuiteSessionsReceiptV1) || !strings.Contains(err.Error(), "evaluate again with --development-host FILE --bindings FILE") {
				t.Fatalf("receipt v1 refusal = %v", err)
			}
		})
	}
	if f.calls != 0 {
		t.Fatalf("refusal reached the cluster: %v", f.calls)
	}
}

// The common App entry point delegates the AgentSessions render and keeps its
// binding and model diagnostics.
func TestReleaseLiftAppEntryPointKeepsAgentSessionsRefusal(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.Bindings = writeSessionsLiftBinding(t, suiteSessionsBindingYAML("other-model"))
	a, _, f := nativeFixture(t)
	_, err := a.PrepareReleaseLift(t.Context(), opt)
	if !errors.Is(err, ErrReleaseLift) || !errors.Is(err, agentruntime.ErrAgentSessionsModelMismatch) || !strings.Contains(err.Error(), `serves model "other-model" but the suite pins "fixture-model"`) {
		t.Fatalf("App entry point lost the AgentSessions refusal: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("refusal reached the cluster: %v", f.calls)
	}
}

func TestAgentSessionsReleaseLiftRefusesInvalidSelectorsAndInputs(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*ReleaseLiftOptions)
		want string
	}{
		{"context", func(o *ReleaseLiftOptions) { o.ToContext = "kind-test" }, ""},
		{"namespace", func(o *ReleaseLiftOptions) { o.ToNamespace = "orka-system" }, ""},
		{"inference", func(o *ReleaseLiftOptions) { o.Inference = "provider:inference" }, ""},
		{"promote", func(o *ReleaseLiftOptions) { o.Promote = true }, ""},
		{"evaluation name", func(o *ReleaseLiftOptions) { o.EvaluationName = "dev" }, ""},
		{"no bindings", func(o *ReleaseLiftOptions) { o.Bindings = "" }, ""},
		{"tag image", func(o *ReleaseLiftOptions) { o.HarnessImage = "registry.example.invalid/harnessnode:v1" }, ""},
		{"evidence runtime", func(o *ReleaseLiftOptions) { o.ExpectedRuntime = "orka" }, ""},
		{"kagent bindings", func(o *ReleaseLiftOptions) {
			o.Bindings = writeSessionsLiftBinding(t, "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: KagentBindings\nnamespace: agents\nmodelConfig: {provider: openai, secretRef: {name: k, key: k}}\n")
		}, "agentsessions requires --bindings FILE: one valid AgentSessionsBinding document"},
		{"image whitespace", func(o *ReleaseLiftOptions) { o.HarnessImage = sessionsLiftImage + " " }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := opt
			tc.edit(&o)
			p, err := PrepareAgentSessionsReleaseLift(t.Context(), o)
			if p != nil || !errors.Is(err, ErrReleaseLift) || tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v", err)
			}
		})
	}
}

func TestAgentSessionsReleaseLiftApplyIsRefused(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	prepared, err := PrepareAgentSessionsReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	a, _, f := nativeFixture(t)
	result, err := a.ApplyReleaseLift(t.Context(), prepared, prepared.Summary().PlanDigest)
	if !errors.Is(err, ErrReleaseLiftApply) || !errors.Is(err, ErrAgentSessionsLiftPlanOnly) || result.State != "not-applied" {
		t.Fatalf("plan-only render applied: %v %+v", err, result)
	}
	if f.calls != 0 {
		t.Fatalf("apply reached the cluster: %v", f.calls)
	}
}

func TestReleaseLiftClusterPathRefusesAgentSessionsWithGuidance(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.HarnessImage, opt.ToContext = "", "kind-test"
	a, _, f := nativeFixture(t)
	_, err := a.PrepareReleaseLift(t.Context(), opt)
	if !errors.Is(err, ErrReleaseLift) || !strings.Contains(err.Error(), "--harness-image") {
		t.Fatalf("AgentSessions package reached the cluster path: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("refusal reached the cluster: %v", f.calls)
	}
}

// TestAgentSessionsReleaseLiftRefusesUpdateBeforeReads pins the outcome, not a
// specific guard: --update with --harness-image refuses before any suite,
// evidence or cluster read. The --bindings conflict with --update already
// refuses here, so the AgentSessions selector guard is defense in depth; the
// CLI row in cmd/kmx is the one that fails without it.
func TestAgentSessionsReleaseLiftRefusesUpdateBeforeReads(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.Update = true
	if err := ValidateReleaseLiftOptions(opt); !errors.Is(err, ErrReleaseLift) {
		t.Fatalf("selector validation accepted --update with --harness-image: %v", err)
	}
	withoutBindings := opt
	withoutBindings.Bindings = ""
	if err := ValidateReleaseLiftOptions(withoutBindings); !errors.Is(err, ErrReleaseLift) {
		t.Fatalf("selector validation accepted --update with --harness-image and no binding: %v", err)
	}
	if p, err := PrepareAgentSessionsReleaseLift(t.Context(), opt); p != nil || !errors.Is(err, ErrReleaseLift) {
		t.Fatalf("plan-only render accepted --update: %v", err)
	}
	a, _, f := nativeFixture(t)
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	// errReleaseUpdateCompatibility is only reachable after the suite and
	// evidence are read; its absence shows the refusal came from selectors.
	if p != nil || !errors.Is(err, ErrReleaseLift) || errors.Is(err, errReleaseUpdateCompatibility) {
		t.Fatalf("update reached the AgentSessions inputs: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("update refusal reached the cluster: %v", f.calls)
	}
}
