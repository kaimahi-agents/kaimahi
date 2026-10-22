package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func readFlowFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func flowBindingFixture(t *testing.T) (*App, *workspace.FlowInventory, *nativeReadFixture, SuiteFlowOptions) {
	t.Helper()
	a, _, f := nativeFixture(t)
	f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = "packaged-model"
	in := t.TempDir()
	root := filepath.Join(t.TempDir(), "team")
	writeFile(t, filepath.Join(in, "instructions.md"), "Write facts.")
	writeFile(t, filepath.Join(in, "case.yaml"), "id: facts\ninput: facts\nexpectContains: [facts]\n")
	writeFile(t, filepath.Join(in, "members.json"), `{"schemaVersion":"kmx.members/v1alpha1","members":[{"id":"writer","model":"packaged-model","instructions":"instructions.md","cases":["case.yaml"]},{"id":"reviewer","model":"packaged-model","instructions":"instructions.md","cases":["case.yaml"]}]}`)
	writeFile(t, filepath.Join(in, "flow.json"), `{"schemaVersion":"kmx.flow/v1alpha1","id":"release","mode":"pipeline","entry":"writer","edgeRequirements":[],"steps":[{"id":"draft","member":"writer","input":{"source":"run.input"}},{"id":"review","member":"reviewer","input":{"source":"draft.output","context":"run.input"},"approval":"policy-controlled"}],"cases":[{"id":"facts","input":"facts","assertions":[{"step":"review","check":{"id":"verdict","type":"contains","value":"PASS"}}]}]}`)
	if err := workspace.CreateFlow(t.Context(), root, workspace.FlowCreateOptions{Name: "release-team", MembersFile: filepath.Join(in, "members.json"), FlowFile: filepath.Join(in, "flow.json")}); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.PrepareFlow(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := workspace.LoadFlowInventory(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "kmx.yaml"), "version: 1\nevalTarget: dev\ntargets:\n  dev:\n    context: kind-test\n    namespace: orka-system\n    runtime: orka\n    inference: provider:inference\n")
	bindings := filepath.Join(in, "binding.yaml")
	writeFile(t, bindings, fmt.Sprintf("schemaVersion: kmx.suite-binding/v1alpha1\nsuiteDigest: %s\nruntime: orka\nmembers:\n  writer: {placementRef: dev, modelServingRef: 'provider:inference'}\n  reviewer: {placementRef: dev, modelServingRef: 'provider:inference'}\napproval: {invokes: [], steps: []}\n", inv.Digest))
	return a, inv, f, SuiteFlowOptions{Workspace: root, Destination: "dev", Bindings: bindings, Flow: "release"}
}

func TestFlowBindingExactDestination(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if s.suite != inv.Digest || s.flowDigest != inv.FlowDigest || len(s.members) != 2 || !verificationDigest(s.bindingDigest) {
		t.Fatal("selection lost exact suite/flow/member/binding identity")
	}
	old := s.bindingDigest
	inv.Flow.Cases[0].Input = "changed"
	inv.Members[0].Instructions[0] = 'X'
	if s.flow.Cases[0].Input != "facts" || string(s.members[0].suite.Instructions) != "Write facts." {
		t.Fatal("selection aliases mutable input")
	}
	f.objects["namespace/orka-system"]["metadata"].(map[string]any)["uid"] = "replacement-ns"
	if s.revalidate(t.Context(), a) == nil {
		t.Fatal("namespace replacement accepted")
	}
	_, fresh, _, _ := flowBindingFixture(t)
	inv.Flow.Cases[0].Input = "facts"
	inv.Members[0] = fresh.Members[0]
	next, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if next.bindingDigest == old {
		t.Fatal("namespace incarnation absent from B")
	}
}

func TestFlowBindingStaticRefusals(t *testing.T) {
	for _, kind := range []string{"missing", "extra", "duplicate", "unknown", "null", "alias", "document", "namespace", "cross-context", "inference", "saved-bindings", "evalTarget", "gate", "policy", "runtime", "flow-id"} {
		t.Run(kind, func(t *testing.T) {
			a, inv, f, opt := flowBindingFixture(t)
			raw := readFlowFile(t, opt.Bindings)
			cfg := readFlowFile(t, filepath.Join(opt.Workspace, "kmx.yaml"))
			switch kind {
			case "missing":
				raw = strings.Replace(raw, "  reviewer: {placementRef: dev, modelServingRef: 'provider:inference'}\n", "", 1)
			case "extra":
				raw = strings.Replace(raw, "approval:", "  foreign: {placementRef: dev, modelServingRef: 'provider:inference'}\napproval:", 1)
			case "duplicate":
				raw += "runtime: orka\n"
			case "unknown":
				raw += "unexpected: no\n"
			case "null":
				raw = strings.Replace(raw, "runtime: orka", "runtime: null", 1)
			case "alias":
				raw = strings.Replace(raw, "runtime: orka", "runtime: &r orka", 1)
			case "document":
				raw += "---\n{}\n"
			case "namespace":
				cfg = strings.ReplaceAll(cfg, "    namespace: orka-system\n", "")
			case "cross-context":
				cfg = strings.ReplaceAll(cfg, "kind-test", "other-context")
			case "inference":
				cfg = strings.ReplaceAll(cfg, "provider:inference", "provider:other")
			case "saved-bindings":
				cfg = strings.ReplaceAll(cfg, "    inference: provider:inference", "    bindings: legacy.yaml")
			case "evalTarget":
				cfg = strings.Replace(cfg, "evalTarget: dev", "evalTarget: other", 1) + "  other: {context: remote, namespace: orka-system}\n"
			case "gate":
				inv.Flow.Steps[1].Approval = "required"
			case "policy":
				inv.Members[0].Policy = &workspace.NativePolicy{}
			case "runtime":
				inv.Members[0].Runtime.ID = "kagent"
			case "flow-id":
				opt.Flow = "other"
			}
			writeFile(t, opt.Bindings, raw)
			writeFile(t, filepath.Join(opt.Workspace, "kmx.yaml"), cfg)
			if _, err := a.prepareSuiteFlow(t.Context(), inv, opt); err == nil {
				t.Fatal("unsupported selection accepted")
			}
			if f.calls != 0 {
				t.Fatal("static refusal performed live observations")
			}
		})
	}
}

func TestFlowBindingSemanticAndFreshnessSeparation(t *testing.T) {
	a, inv, f, opt := flowBindingFixture(t)
	s, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	f.objects["secret/model-secret"]["metadata"].(map[string]any)["resourceVersion"] = "2"
	f.objects["providers.core.orka.ai/inference"]["metadata"].(map[string]any)["resourceVersion"] = "2"
	if s.revalidate(t.Context(), a) == nil {
		t.Fatal("in-flight rotation not fenced")
	}
	next, err := a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if next.bindingDigest != s.bindingDigest {
		t.Fatal("incidental RV changed B")
	}
	f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["baseURL"] = "https://other.example.invalid/v1"
	next, err = a.prepareSuiteFlow(t.Context(), inv, opt)
	if err != nil {
		t.Fatal(err)
	}
	if next.bindingDigest == s.bindingDigest {
		t.Fatal("endpoint change absent from B")
	}
	f.objects["providers.core.orka.ai/inference"]["spec"].(map[string]any)["defaultModel"] = "other"
	if _, err := a.prepareSuiteFlow(t.Context(), inv, opt); err == nil {
		t.Fatal("declared model mismatch accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.prepareSuiteFlow(ctx, inv, opt); err != context.Canceled {
		t.Fatal("cancellation lost")
	}
}
