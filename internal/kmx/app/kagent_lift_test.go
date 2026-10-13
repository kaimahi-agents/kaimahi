package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func kagentLiftFixture(t *testing.T, scenario string) (*App, LiftAgentBundleOptions, string) {
	t.Helper()
	return kagentLiftFixtureMode(t, scenario, true)
}

func kagentLiftFixtureMode(t *testing.T, scenario string, inProcess bool) (*App, LiftAgentBundleOptions, string) {
	t.Helper()
	a, create, _, _, dir := kagentCreateFixtureMode(t, scenario, inProcess)
	profile := kagentLiftProfile()
	profile.AgentSchema = a.kagentProfile.AgentSchema
	profile.ModelSchema = a.kagentProfile.ModelSchema
	profile.RemoteSchema = profile.AgentSchema
	a.kagentProfile = &profile
	source, bindings, err := portableKagentSource(create, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := agentruntime.EncodeKagentBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKagentBundle(create.BundlePath, source, encoded); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_KAGENT_TEST_VERSION", "v0.10.3")
	t.Setenv("KMX_HOME", filepath.Join(dir, "state"))
	return a, LiftAgentBundleOptions{BundleDir: create.BundlePath, Runtime: "kagent", RuntimeVersion: "v0.10.3", ToContext: "kind-test"}, dir
}

func TestKagentLiftRejectsDestinationRebindingBeforeAdmission(t *testing.T) {
	for _, plan := range []bool{false, true} {
		t.Run(map[bool]string{false: "lift", true: "plan"}[plan], func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "selected-cluster-rebound")
			opt.Plan = plan
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), "selected destination cluster") {
				t.Fatalf("receipt selection rebound into another cluster: %v", err)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir))
			assertNoKagentLiftReceipt(t, opt.BundleDir)
		})
	}
}

func TestKagentLiftRequiresCurrentReplicaSetOwnerChain(t *testing.T) {
	for _, scenario := range []string{"pod-owner-missing", "pod-owner-foreign", "pod-owner-old", "pod-owner-duplicate", "foreign-matching-pod", "rs-owner-foreign", "rs-revision-old", "rs-template-mutated", "rs-identity-missing", "controller-pod-owner-foreign"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _ := kagentLiftFixture(t, scenario)
			err := a.LiftAgentBundle(opt)
			if err == nil || (!strings.Contains(err.Error(), "owner") && !strings.Contains(err.Error(), "ReplicaSet")) {
				t.Fatalf("unproven current Pod->ReplicaSet->Deployment chain accepted: %v", err)
			}
			assertNoKagentLiftReceipt(t, opt.BundleDir)
		})
	}
}

func TestKagentDeploymentTemplateFingerprintPreservesInt64(t *testing.T) {
	deployment := json.RawMessage(`{"template":{"metadata":{"labels":{"app":"kagent"}},"spec":{"securityContext":{"runAsUser":9007199254740992}}}}`)
	replicaSet := json.RawMessage(`{"template":{"metadata":{"labels":{"app":"kagent","pod-template-hash":"current"}},"spec":{"securityContext":{"runAsUser":9007199254740993}}}}`)
	want, err := kagentDeploymentTemplateFingerprint(deployment)
	if err != nil {
		t.Fatal(err)
	}
	got, err := kagentDeploymentTemplateFingerprint(replicaSet)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("adjacent int64 security identities produced equal pod-template fingerprints")
	}
	matching := bytes.Replace(replicaSet, []byte("9007199254740993"), []byte("9007199254740992"), 1)
	got, err = kagentDeploymentTemplateFingerprint(matching)
	if err != nil || got != want {
		t.Fatalf("matching int64 template with controller-added hash label rejected: %v", err)
	}
	if _, err := kagentDeploymentTemplateFingerprint(append(deployment, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted as a pod-template spec")
	}
}

func TestKagentLiftRejectsAdjacentInt64ReplicaSetTemplate(t *testing.T) {
	a, opt, _ := kagentLiftFixture(t, "rs-template-adjacent-int64")
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "current ReplicaSet template or selector differs") {
		t.Fatalf("adjacent int64 ReplicaSet template authorized lift: %v", err)
	}
	assertNoKagentLiftReceipt(t, opt.BundleDir)
}

func TestKagentLiftFinalReadinessLossWritesNoReceipt(t *testing.T) {
	for _, scenario := range []string{"final-model-rejected", "final-agent-rejected", "final-agent-notready", "final-agent-stale"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _ := kagentLiftFixture(t, scenario)
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), "current-generation") {
				t.Fatalf("final readiness loss accepted: %v", err)
			}
			assertNoKagentLiftReceipt(t, opt.BundleDir)
		})
	}
}

func TestKagentStatusFinalReadinessAndWorkloadCategories(t *testing.T) {
	for _, tc := range []struct {
		scenario, state string
		change          func(*testing.T, string)
	}{
		{"final-model-rejected", "failed", nil}, {"final-agent-rejected", "failed", nil}, {"final-agent-notready", "failed", nil}, {"final-agent-stale", "failed", nil},
		{"workload-unreadable", "unreadable", nil}, {"workload-malformed", "unreadable", nil}, {"workload-identity-incomplete", "unreadable", nil}, {"service-unreadable", "unreadable", nil}, {"service-malformed", "unreadable", nil}, {"service-owner-foreign", "drift", nil}, {"generated-secret-malformed", "unreadable", nil}, {"generated-account-unreadable", "unreadable", nil}, {"node-unreadable", "unreadable", nil}, {"node-malformed", "unreadable", nil}, {"agent-platform-wrong-imageid", "drift", nil}, {"pods-unreadable", "unreadable", nil}, {"pods-malformed", "unreadable", nil}, {"rs-unreadable", "unreadable", nil}, {"rs-malformed", "unreadable", nil}, {"generated-secret-unreadable", "unreadable", nil},
		{"workload-owner-foreign", "drift", nil}, {"generated-secret-owner-foreign", "drift", nil}, {"pod-owner-foreign", "drift", nil}, {"pod-owner-old", "drift", nil}, {"foreign-matching-pod", "drift", nil},
		{"pods-missing-items", "unreadable", nil}, {"pods-null-list", "unreadable", nil}, {"pods-null-items", "unreadable", nil}, {"pods-empty", "failed", nil},
		{"workload-notready", "failed", nil}, {"pod-notready", "failed", nil},
		{"workload-security-mutated", "drift", nil}, {"workload-sidecar-mutated", "drift", nil}, {"workload-template-mutated", "drift", nil},
		{"later-live-mutated", "drift", nil}, {"dependency-drift", "drift", nil}, {"dependency-missing-key", "drift", nil}, {"stale-agent", "failed", nil},
		{"replacement", "drift", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "sample-agents.kagent.dev.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			obj["metadata"].(map[string]any)["uid"] = "replacement-agent-uid"
			raw, err = json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"partial", "drift", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "sample-modelconfigs.kagent.dev.json")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, dir)
			}
			_ = os.Remove(filepath.Join(dir, "workload-read"))
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", tc.scenario)
			before := len(kagentCalls(t, dir))
			got := readKagentStatusForTest(t, a, opt)
			if got.State != tc.state || got.Ready {
				t.Fatalf("status=%+v, want %s", got, tc.state)
			}
			_ = os.Remove(filepath.Join(dir, "workload-read"))
			if err := a.LiftAgentBundle(opt); err == nil {
				t.Fatal("failed/unreadable/drifting proof authorized reuse")
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestKagentLiftVerifiedKubeletConfigDigestStatusImage(t *testing.T) {
	for _, scenario := range []string{"status-image-config", "status-image-config-arm64"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, scenario)
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatalf("verified kubelet config digest rejected: %v", err)
			}
			before := len(kagentCalls(t, dir))
			if got := readKagentStatusForTest(t, a, opt); got.State != "ready" {
				t.Fatalf("verified config digest status=%+v", got)
			}
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatalf("verified config digest reuse: %v", err)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestKagentLiftConfigDigestDoesNotReplaceImageIdentityProof(t *testing.T) {
	for _, scenario := range []string{"status-image-config-unknown", "status-image-config-wrong-platform", "status-image-config-wrong-repository", "status-image-config-wrong-imageid", "status-image-config-wrong-pod-reference"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, scenario)
			err := a.LiftAgentBundle(opt)
			if err == nil || (!strings.Contains(err.Error(), "image") && !strings.Contains(err.Error(), "container using")) {
				t.Fatalf("invalid image evidence accepted or unrelated refusal: %v", err)
			}
			assertKagentNoClusterWrites(t, dir)
		})
	}
}

func TestKagentLiftConsentRetryPreservesBundleInputs(t *testing.T) {
	for _, plan := range []bool{false, true} {
		t.Run(map[bool]string{false: "lift", true: "plan"}[plan], func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "remote-context")
			opt.Bindings = filepath.Join(dir, "external bindings.yaml")
			if err := os.Rename(filepath.Join(opt.BundleDir, "bindings.yaml"), opt.Bindings); err != nil {
				t.Fatal(err)
			}
			opt.Plan = plan
			err := a.LiftAgentBundle(opt)
			want := "KAIMAHI_CONFIRM=kind-test kmx --context kind-test bundle lift " + opt.BundleDir + " --runtime kagent --runtime-version v0.10.3 --to-context kind-test --to-namespace agents --bindings '" + opt.Bindings + "'"
			if plan {
				want += " --plan"
			}
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), " create ") {
				t.Fatalf("consent retry lost the explicit bundle operation: %v; want %s", err, want)
			}
			calls := kagentCalls(t, dir)
			if len(calls) != 1 || !slices.Contains(calls[0].Args, "config") {
				t.Fatalf("refused consent reached installation reads or admission: %+v", calls)
			}
		})
	}
}

func TestKagentCreateConsentRetryRemainsPromptFree(t *testing.T) {
	a, opt, _, diagnostics, dir := kagentCreateFixture(t, "remote-context")
	opt.Task = "private task must not be echoed"
	a.InvocationCommand = "kmx create --live sample --task " + opt.Task
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "KAIMAHI_CONFIRM=kind-test kmx --context kind-test create --live sample --runtime kagent") {
		t.Fatalf("historical create consent retry changed: %v", err)
	}
	if strings.Contains(err.Error(), opt.Task) || strings.Contains(diagnostics.String(), opt.Task) {
		t.Fatal("create guard echoed the private task")
	}
	for _, disclosure := range []string{"stores the full task prompt, history, and answer", "session retention is unlimited", "audit policy may capture"} {
		if !strings.Contains(diagnostics.String(), disclosure) {
			t.Fatalf("create consent lost disclosure %q: %s", disclosure, diagnostics)
		}
	}
	assertKagentNoClusterWrites(t, dir)
}

func TestKagentLiftApprovedDigestReferences(t *testing.T) {
	a, opt, _ := kagentLiftFixture(t, "digest-images")
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatalf("approved official digest reference rejected: %v", err)
	}
}

func TestKagentLiftPlanAndNewThenReadOnlyRepeat(t *testing.T) {
	a, opt, dir := kagentLiftFixture(t, "")
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	assertKagentNoClusterWrites(t, dir)
	if _, err := os.Stat(filepath.Join(opt.BundleDir, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("plan wrote receipts: %v", err)
	}
	opt.Plan = false
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig", "Agent")
	entries, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts", "kagent-v0.10.3"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("receipts=%v err=%v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(opt.BundleDir, "receipts", "kagent-v0.10.3", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var wrapped bundleLiftReceipt
	if json.Unmarshal(raw, &wrapped) != nil || len(wrapped.Receipt.Resources) != 2 {
		t.Fatalf("receipt=%s", raw)
	}
	if len(wrapped.Receipt.Workload) != 4 || len(wrapped.Receipt.Workload[0].SpecDigest) != 64 {
		t.Fatalf("complete Deployment spec fingerprint missing: %+v", wrapped.Receipt.Workload)
	}
	if bytes.Contains(raw, []byte("secret-hash")) || bytes.Contains(raw, []byte("Answer briefly")) {
		t.Fatal("receipt leaked secret hash or instructions")
	}
	before := len(kagentCalls(t, dir))
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("kagent wrote Orka state: %v", err)
	}
	if got := readKagentStatusForTest(t, a, opt); got.State != "ready" || !got.Ready || got.AgentUID != "agent-uid" || got.DeploymentUID != "agent-deployment-uid" {
		t.Fatalf("status=%+v", got)
	}
}

func readKagentStatusForTest(t *testing.T, a *App, opt LiftAgentBundleOptions) kagentBundleTargetStatus {
	t.Helper()
	a.Out.(*bytes.Buffer).Reset()
	if err := a.BundleStatus(BundleStatusOptions{BundleDir: opt.BundleDir, Runtime: "kagent", RuntimeVersion: "v0.10.3", Context: opt.ToContext, Bindings: opt.Bindings, Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var report kagentBundleStatusReport
	if err := json.Unmarshal(a.Out.(*bytes.Buffer).Bytes(), &report); err != nil || len(report.Targets) != 1 {
		t.Fatalf("status=%s err=%v", a.Out.(*bytes.Buffer).Bytes(), err)
	}
	return report.Targets[0]
}

func TestKagentLiftStatusAbsenceUnreadableAndUnbound(t *testing.T) {
	for _, scenario := range []string{"", "primary-unreadable", "child-unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, scenario)
			got := readKagentStatusForTest(t, a, opt)
			want := "absent"
			if scenario != "" {
				want = "unreadable"
			}
			if got.State != want || got.Ready || !got.NoReceipt {
				t.Fatalf("status=%+v", got)
			}
			assertKagentNoClusterWrites(t, dir)
		})
	}
	a, opt, dir := kagentLiftFixture(t, "")
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, kagentLiftReceiptDir, kagentLiftReceiptName(opt.ToContext, "agents", "cluster-uid"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	before := len(kagentCalls(t, dir))
	got := readKagentStatusForTest(t, a, opt)
	if got.Ready || got.State != "failed" || !got.NoReceipt || !strings.Contains(got.Detail, "unbound") {
		t.Fatalf("missing receipt status=%+v", got)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "without a complete matching receipt") {
		t.Fatalf("missing receipt authorized reuse: %v", err)
	}
	assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
}

func TestKagentLiftReceiptAndLockSafety(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, LiftAgentBundleOptions)
	}{
		{"locked", "operation locked", func(t *testing.T, o LiftAgentBundleOptions) {
			if err := os.Mkdir(filepath.Join(o.BundleDir, kagentLiftLock), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"receipt directory symlink", "private directories", func(t *testing.T, o LiftAgentBundleOptions) {
			if err := os.Symlink(t.TempDir(), filepath.Join(o.BundleDir, "receipts")); err != nil {
				t.Fatal(err)
			}
		}},
		{"receipt malformed", "receipt conflict", func(t *testing.T, o LiftAgentBundleOptions) {
			dir := filepath.Join(o.BundleDir, kagentLiftReceiptDir)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, kagentLiftReceiptName(o.ToContext, "agents", "cluster-uid")), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"receipt symlink", "receipt conflict", func(t *testing.T, o LiftAgentBundleOptions) {
			dir := filepath.Join(o.BundleDir, kagentLiftReceiptDir)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "receipt")
			if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, kagentLiftReceiptName(o.ToContext, "agents", "cluster-uid"))); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			tc.setup(t, opt)
			if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("safety error=%v", err)
			}
			assertKagentNoClusterWrites(t, dir)
		})
	}
}

func TestKagentLiftMCPDependenciesBoundAndRedacted(t *testing.T) {
	for _, scenario := range []string{"", "tool-replaced", "tool-spec-drift", "tool-secret-drift", "tool-set-drift", "wrong-remote-schema"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, scenario)
			path := filepath.Join(opt.BundleDir, "agent.yaml")
			raw, _ := os.ReadFile(path)
			raw = append(raw, []byte("        tools:\n            - server:\n                kind: RemoteMCPServer\n                name: toolbox\n              toolNames:\n                - read\n                - list\n")...)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			err := a.LiftAgentBundle(opt)
			if scenario != "" {
				if err == nil || (!strings.Contains(err.Error(), "RemoteMCPServer") && !strings.Contains(err.Error(), "dependency identity")) {
					t.Fatalf("MCP drift=%v", err)
				}
				assertNoKagentLiftReceipt(t, opt.BundleDir)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			receiptPath := filepath.Join(opt.BundleDir, kagentLiftReceiptDir, kagentLiftReceiptName(opt.ToContext, "agents", "cluster-uid"))
			receiptRaw, _ := os.ReadFile(receiptPath)
			var receipt bundleLiftReceipt
			if json.Unmarshal(receiptRaw, &receipt) != nil || len(receipt.Receipt.Dependencies) != 2 || receipt.Receipt.Dependencies[1].Kind != "RemoteMCPServer" {
				t.Fatalf("MCP receipt=%s", receiptRaw)
			}
			if bytes.Contains(receiptRaw, []byte("secret-hash")) || bytes.Contains(receiptRaw, []byte("example.invalid")) {
				t.Fatal("MCP receipt persisted secret hash or endpoint")
			}
			before := len(kagentCalls(t, dir))
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatal(err)
			}
			assertKagentReadOnlyCalls(t, kagentCalls(t, dir)[before:])
		})
	}
}

func TestKagentLiftRefusesPresentPromotionPolicyBeforeTargetReads(t *testing.T) {
	a, opt, dir := kagentLiftFixture(t, "")
	if err := os.WriteFile(filepath.Join(opt.BundleDir, "lift-policy.yaml"), []byte("rules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "lift-policy.yaml") {
		t.Fatalf("promotion policy was ignored: %v", err)
	}
	if len(kagentCalls(t, dir)) != 0 {
		t.Fatal("policy refusal reached target")
	}
}

func TestKagentStatusUsesExplicitExternalBindings(t *testing.T) {
	a, opt, dir := kagentLiftFixture(t, "")
	external := filepath.Join(dir, "target-bindings.yaml")
	raw, _ := os.ReadFile(filepath.Join(opt.BundleDir, "bindings.yaml"))
	if err := os.WriteFile(external, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opt.Bindings = external
	if err := os.Remove(filepath.Join(opt.BundleDir, "bindings.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if got := readKagentStatusForTest(t, a, opt); got.State != "ready" {
		t.Fatalf("external bindings=%+v", got)
	}
	before := len(kagentCalls(t, dir))
	err := a.BundleStatus(BundleStatusOptions{BundleDir: opt.BundleDir, Runtime: "kagent", RuntimeVersion: "v0.10.3", Context: opt.ToContext, Output: "json"})
	if err == nil || len(kagentCalls(t, dir)) != before {
		t.Fatalf("missing external bindings inferred live input: %v", err)
	}
	// Bindings carry provider/endpoint, not model: vary an explicit endpoint.
	bindings, parseErr := agentruntime.ParseKagentBindings(raw)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	bindings.ModelConfig.BaseURL = "http://model.example.invalid/v1"
	changed, err := agentruntime.EncodeKagentBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if got := readKagentStatusForTest(t, a, opt); got.State != "drift" {
		t.Fatalf("changed bindings=%+v", got)
	}
}

func TestKagentPythonLiftAndStatusAreRenderOnlyRefusals(t *testing.T) {
	a, opt, dir := kagentLiftFixture(t, "")
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte("runtime: go"), []byte("runtime: python"), 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "render-only") {
		t.Fatalf("Python lift=%v", err)
	}
	if err := a.BundleStatus(BundleStatusOptions{BundleDir: opt.BundleDir, Runtime: "kagent", RuntimeVersion: "v0.10.3", Context: opt.ToContext}); err == nil || !strings.Contains(err.Error(), "render-only") {
		t.Fatalf("Python status=%v", err)
	}
	if len(kagentCalls(t, dir)) != 0 {
		t.Fatal("unqualified Python reached cluster")
	}
}

// Exercise the public workflows on one neutral bundle, not just receipt readers.
func TestMixedRuntimeCoreOnlyPublicBundleWorkflows(t *testing.T) {
	orka, orkaOpt, orkaDir, _ := liftBundleFixture(t)
	orka.Out = &bytes.Buffer{}
	kagent, kagentOpt, kagentDir := kagentLiftFixture(t, "")
	source, err := os.ReadFile(filepath.Join(kagentOpt.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(source, []byte("extensions:"))
	if index < 0 {
		t.Fatal("fixture has no extension")
	}
	if err := os.WriteFile(filepath.Join(orkaOpt.BundleDir, "agent.yaml"), source[:index], 0600); err != nil {
		t.Fatal(err)
	}
	kagentOpt.Bindings = filepath.Join(kagentOpt.BundleDir, "bindings.yaml")
	kagentOpt.BundleDir = orkaOpt.BundleDir
	_, _, _, rendered, _, err := kagent.prepareKagentBundle(kagentOpt.BundleDir, kagentOpt.Bindings, kagentOpt.ToContext, "")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.PortableDigest() != agentruntime.PortableBundleDigest(source[:index]) || len(rendered.Documents()) != 3 || len(rendered.DeployDocuments()) != 2 {
		t.Fatal("render/digest or review-only Secret contract changed")
	}
	bundle, err := kagentBundleFromRendered(rendered)
	if err != nil || bundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"] != "go" {
		t.Fatalf("core default is not go: %v", err)
	}
	home := filepath.Join(orkaDir, "user-state")
	t.Setenv("KMX_HOME", home)
	selectionPath, err := bundleLiftSelectionPath(orkaOpt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	selection := bundleLiftSelection{Context: "kind-test", Namespace: OrkaNamespace, ClusterUID: "cluster-uid", Inference: "provider:inference"}
	if err := saveBundleLiftSelection(selectionPath, selection); err != nil {
		t.Fatal(err)
	}
	selectionBefore, err := os.ReadFile(selectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := kagent.LiftAgentBundle(kagentOpt); err != nil {
		t.Fatal(err)
	}
	kagentReceiptPath := filepath.Join(kagentOpt.BundleDir, kagentLiftReceiptDir, kagentLiftReceiptName("kind-test", "agents", "cluster-uid"))
	kagentReceipt, err := os.ReadFile(kagentReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	// A flat foreign receipt must not influence Orka's target/namespace discovery.
	if err := writeLiftReceipt(orkaOpt.BundleDir, "foreign-cluster", bundleLiftReceipt{Receipt: agentruntime.DeployReceipt{Bundle: "sample", Target: agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: "kind-test", Namespace: "foreign"}}}); err != nil {
		t.Fatal(err)
	}
	foreignPath := bundleReceiptPath(orkaOpt.BundleDir, "kind-test", "foreign", "foreign-cluster")
	if _, ok := readBundleReceiptAt(foreignPath); ok {
		t.Fatal("Orka retirement accepted kagent receipt")
	}
	foreignBefore, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	useOrka := func() { t.Setenv("PATH", orkaDir) }
	useKagent := func() { t.Setenv("PATH", kagentDir) }
	useOrka()
	// No runtime, destination or inference flags: remembered Orka selection only.
	plainLift := LiftAgentBundleOptions{BundleDir: orkaOpt.BundleDir}
	if err := orka.LiftAgentBundle(plainLift); err != nil {
		t.Fatal(err)
	}
	orkaReceiptPath := bundleReceiptPath(orkaOpt.BundleDir, "kind-test", OrkaNamespace, "cluster-uid")
	raw, err := os.ReadFile(orkaReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var historical bundleLiftReceipt
	if err := json.Unmarshal(raw, &historical); err != nil {
		t.Fatal(err)
	}
	historical.Receipt.Target.Runtime = "" // Pre-runtime receipt compatibility.
	raw, err = json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orkaReceiptPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, orkaDir))
	orka.Out.(*bytes.Buffer).Reset()
	if err := orka.BundleStatus(BundleStatusOptions{BundleDir: orkaOpt.BundleDir, Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var report bundleStatusReport
	if err := json.Unmarshal(orka.Out.(*bytes.Buffer).Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].Namespace != OrkaNamespace || report.Targets[0].State != bundleStateInSync {
		t.Fatalf("no-flag status consumed foreign history: %+v", report.Targets)
	}
	for _, call := range orkaCalls(t, orkaDir)[before:] {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") && !(call.Document["kind"] == "SubjectAccessReview" && slices.Contains(call.Args, "--raw")) {
			t.Fatalf("no-flag status wrote a resource: %+v", call)
		}
	}
	if err := orka.LiftAgentBundle(plainLift); err != nil {
		t.Fatalf("historical Orka lift: %v", err)
	}
	// Keep legacy history present for explicit Kagent status and no-flag retire,
	// rather than letting the preceding Orka repeat replace it with a new receipt.
	if err := os.WriteFile(orkaReceiptPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	useKagent()
	if got := readKagentStatusForTest(t, kagent, kagentOpt); got.State != "ready" || got.Namespace != "agents" {
		t.Fatalf("explicit Kagent status consumed Orka history: %+v", got)
	}
	if after, err := os.ReadFile(selectionPath); err != nil || !bytes.Equal(after, selectionBefore) {
		t.Fatalf("Kagent changed remembered Orka selection: %v", err)
	}
	useOrka()
	if err := orka.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: orkaOpt.BundleDir}); err != nil {
		t.Fatalf("no-flag Orka retire: %v", err)
	}
	for _, kind := range []string{"agents", "providers"} {
		if _, err := os.Stat(filepath.Join(orkaDir, kind+".core.orka.ai.json")); !os.IsNotExist(err) {
			t.Fatalf("Orka retire left %s: %v", kind, err)
		}
	}
	for path, want := range map[string][]byte{kagentReceiptPath: kagentReceipt, foreignPath: foreignBefore} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, want) {
			t.Fatalf("Orka workflow consumed foreign receipt %s: %v", path, err)
		}
	}
	useKagent()
	if got := readKagentStatusForTest(t, kagent, kagentOpt); got.State != "ready" {
		t.Fatalf("Orka retirement changed Kagent placement: %+v", got)
	}
}

func fmtKind(body map[string]any) string { kind, _ := body["kind"].(string); return kind }

func TestKagentLiftExplicitSelectionRefusesBeforeClusterReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*LiftAgentBundleOptions)
	}{
		{"version missing", func(o *LiftAgentBundleOptions) { o.RuntimeVersion = "" }},
		{"version other", func(o *LiftAgentBundleOptions) { o.RuntimeVersion = "v0.10.2" }},
		{"context missing", func(o *LiftAgentBundleOptions) { o.ToContext = "" }},
		{"inference", func(o *LiftAgentBundleOptions) { o.Inference = "provider:other" }},
		{"gate", func(o *LiftAgentBundleOptions) { o.RequireEvaluated = "yes" }},
		{"namespace", func(o *LiftAgentBundleOptions) { o.ToNamespace = "other" }},
		{"bindings missing", func(o *LiftAgentBundleOptions) { o.Bindings = "missing.yaml" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			tc.change(&opt)
			if err := a.LiftAgentBundle(opt); err == nil {
				t.Fatal("invalid selection accepted")
			}
			if len(kagentCalls(t, dir)) != 0 {
				t.Fatal("invalid selection reached target reads")
			}
		})
	}
}

func TestKagentLiftFailClosedWritesNoReceipt(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"model-loses-acceptance", "Accepted"},
		{"wrong-version", "exact v0.10.3"}, {"dry-run-mutated", "mutated"},
		{"child-secret-collision", "already exists"}, {"unknown-create", "not retried"},
		{"later-live-mutated", "admitted spec"}, {"wrong-agent-imageid", "digest"},
		{"final-cluster-drift", "cluster"}, {"final-platform-drift", "changed"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, tc.scenario)
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			assertNoKagentLiftReceipt(t, opt.BundleDir)
			if tc.scenario == "unknown-create" || tc.scenario == "model-loses-acceptance" {
				assertKagentWriteKinds(t, kagentCalls(t, dir), "ModelConfig")
			}
		})
	}
}

func assertKagentWriteKinds(t *testing.T, calls []kagentCall, want ...string) {
	t.Helper()
	var writes []string
	for _, call := range calls {
		if call.Body != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes = append(writes, fmtKind(call.Body))
		}
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("actual ordered writes = %v, want %v", writes, want)
	}
}

func assertKagentReadOnlyCalls(t *testing.T, calls []kagentCall) {
	t.Helper()
	for _, call := range calls {
		if call.Body != nil {
			t.Fatalf("read-only operation attempted admission or mutation: %+v", call)
		}
	}
}

func assertNoKagentLiftReceipt(t *testing.T, bundle string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(bundle, kagentLiftReceiptDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("failed lift wrote receipt %s", entry.Name())
		}
	}
}
