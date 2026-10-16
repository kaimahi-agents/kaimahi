package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const appDenyAllPolicy = `{"network":{"egressAllow":[]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`

func addWorkspacePolicy(t *testing.T, root string) {
	t.Helper()
	name := filepath.Join(root, "agents/greeter/member.yaml")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, name, string(raw)+"policy: policy.json\n")
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), appDenyAllPolicy)
}

func TestDeclaredPolicyRefusesEvaluationAndComposedLiftBeforeOutput(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	addWorkspacePolicy(t, opt.Workspace)
	// An invalid output location would fail differently if output preflight ran.
	opt.Out = filepath.Join(opt.Workspace, "forbidden.json")
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if p != nil {
		p.Close()
		t.Fatal("declared policy evaluation prepared")
	}
	if !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("policy refusal lost: %v", err)
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("refusal created output")
	}
	opt.Out = ""
	lift, err := PrepareWorkspaceLift(t.Context(), WorkspaceLiftOptions{Workspace: opt.Workspace, Evaluation: opt, Destination: ReleaseLiftOptions{ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference"}, EvalName: "dev", DestinationName: "prod", Repository: "registry.example/agents", PlanOnly: true})
	if lift != nil {
		lift.Close()
		t.Fatal("composed Lift prepared declared policy")
	}
	if !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("composed policy refusal lost: %v", err)
	}
}
func TestDeclaredPolicyRefusesPrivateNativePreparationBeforeReads(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	addWorkspacePolicy(t, opt.Workspace)
	p, err := workspace.Prepare(t.Context(), opt.Workspace, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	a, _, reads := nativeFixture(t)
	before := reads.calls
	if _, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference"); !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("private preparation ignored policy: %v", err)
	}
	if _, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", strings.Repeat("a", 32)); !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("candidate preparation ignored policy: %v", err)
	}
	if reads.calls != before {
		t.Fatal("policy refusal reached cluster reads")
	}
}
func TestDeclaredPolicyRefusesReceiptAndReleaseLiftBeforeReads(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(privateSuiteDir(t), "old-pass.json")
	if err := os.WriteFile(evidence, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	addWorkspacePolicy(t, root)
	candidate, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	declared, err := workspace.Load(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalSuitePassingReceipt(t.Context(), receipt, declared); !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("receipt accepted policy: %v", err)
	}
	a, _, reads := nativeFixture(t)
	before := reads.calls
	_, err = a.PrepareReleaseLift(t.Context(), ReleaseLiftOptions{Reference: "suite@" + declared.Digest, FromLayout: candidate.Path, ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", Evidence: evidence})
	if !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
		t.Fatalf("Lift policy refusal lost: %v", err)
	}
	if reads.calls != before {
		t.Fatal("policy Lift reached target observations")
	}
	for _, mode := range []string{"managed", "explicit"} {
		layout := filepath.Join(t.TempDir(), "refused-layout")
		if mode == "managed" {
			_, err = a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: root, Reference: "team:v1", Layout: layout})
		} else {
			_, err = a.PushSuiteWorkspaceEvaluated(t.Context(), root, "team:v1", layout, false, false, SuitePushProvenance{}, evidence)
		}
		if !errors.Is(err, workspace.ErrPolicyExecutionUnsupported) {
			t.Fatalf("%s evaluated push policy refusal lost: %v", mode, err)
		}
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatal("refused evaluated push transferred content")
		}
	}
}
func TestDeclaredPolicyUnevaluatedPushPreservesGraph(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	addWorkspacePolicy(t, opt.Workspace)
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	layout := filepath.Join(t.TempDir(), "layout")
	result, err := a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: opt.Workspace, Reference: "team:v1", Layout: layout, Unevaluated: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, result.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := workspace.LoadArtifactInventory(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Members) != 1 || inv.Members[0].Policy == nil || string(inv.Members[0].Policy.Bytes) != appDenyAllPolicy {
		t.Fatal("unevaluated publication dropped policy")
	}
}
