package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	name := agentsessions.TemplateName(p.suite.Name, p.suite.MemberID, "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae", plan.BindingDigest)
	tmpl := plan.ActorTemplate
	if tmpl.Metadata != (agentsessions.ActorMetadata{Atespace: "prod", Name: name}) || tmpl.SandboxConfig.SandboxClass != "SANDBOX_CLASS_GVISOR" {
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

func TestAgentSessionsReleaseLiftRefusesModelMismatchClearly(t *testing.T) {
	opt, _, _ := sessionsLiftFixture(t)
	opt.Bindings = writeSessionsLiftBinding(t, suiteSessionsBindingYAML("other-model"))
	_, err := PrepareAgentSessionsReleaseLift(t.Context(), opt)
	if !errors.Is(err, ErrReleaseLift) || !errors.Is(err, agentruntime.ErrAgentSessionsModelMismatch) || !strings.Contains(err.Error(), `serves model "other-model" but the suite pins "fixture-model"`) {
		t.Fatalf("model mismatch planned or unclear: %v", err)
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
