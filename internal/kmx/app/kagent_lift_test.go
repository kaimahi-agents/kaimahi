package app

import (
	"bytes"
	"encoding/json"
	"errors"
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
	a, create, _, _, dir := kagentCreateFixture(t, scenario)
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
			for _, call := range kagentCalls(t, dir) {
				if call.Body != nil {
					t.Fatal("rebound destination reached dry-run or write")
				}
			}
			entries, _ := os.ReadDir(filepath.Join(opt.BundleDir, kagentLiftReceiptDir))
			if len(entries) != 0 {
				t.Fatal("rebound destination wrote receipt")
			}
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
			entries, _ := os.ReadDir(filepath.Join(opt.BundleDir, kagentLiftReceiptDir))
			if len(entries) != 0 {
				t.Fatal("unowned workload wrote success receipt")
			}
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
	entries, err := os.ReadDir(filepath.Join(opt.BundleDir, kagentLiftReceiptDir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("mismatched security identity wrote success receipt")
	}
}

func TestKagentLiftFinalReadinessLossWritesNoReceipt(t *testing.T) {
	for _, scenario := range []string{"final-model-rejected", "final-agent-rejected", "final-agent-notready", "final-agent-stale"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _ := kagentLiftFixture(t, scenario)
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), "current-generation") {
				t.Fatalf("final readiness loss accepted: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Join(opt.BundleDir, kagentLiftReceiptDir))
			if len(entries) != 0 {
				t.Fatal("final rejection wrote success receipt")
			}
		})
	}
}

func TestKagentStatusFinalReadinessAndWorkloadCategories(t *testing.T) {
	for _, tc := range []struct{ scenario, state string }{
		{"final-model-rejected", "failed"}, {"final-agent-rejected", "failed"}, {"final-agent-notready", "failed"}, {"final-agent-stale", "failed"},
		{"workload-unreadable", "unreadable"}, {"workload-malformed", "unreadable"}, {"workload-identity-incomplete", "unreadable"}, {"service-unreadable", "unreadable"}, {"service-malformed", "unreadable"}, {"service-owner-foreign", "drift"}, {"generated-secret-malformed", "unreadable"}, {"generated-account-unreadable", "unreadable"}, {"node-unreadable", "unreadable"}, {"node-malformed", "unreadable"}, {"agent-platform-wrong-imageid", "drift"}, {"pods-unreadable", "unreadable"}, {"pods-malformed", "unreadable"}, {"rs-unreadable", "unreadable"}, {"rs-malformed", "unreadable"}, {"generated-secret-unreadable", "unreadable"},
		{"workload-owner-foreign", "drift"}, {"generated-secret-owner-foreign", "drift"}, {"pod-owner-foreign", "drift"}, {"pod-owner-old", "drift"}, {"foreign-matching-pod", "drift"},
		{"pods-missing-items", "unreadable"}, {"pods-null-list", "unreadable"}, {"pods-null-items", "unreadable"}, {"pods-empty", "failed"},
		{"workload-notready", "failed"}, {"pod-notready", "failed"},
		{"workload-security-mutated", "drift"}, {"workload-sidecar-mutated", "drift"}, {"workload-template-mutated", "drift"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatal(err)
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
			for _, call := range kagentCalls(t, dir)[before:] {
				if call.Body != nil {
					t.Fatal("status/reuse admitted or wrote")
				}
			}
		})
	}
}

func TestKagentCreateKeepsHistoricalPodFixtureContract(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "pod-owner-missing")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("v0.10.2 fixture contract changed: %v", err)
	}
	for _, call := range kagentCalls(t, dir) {
		if slices.Contains(call.Args, "replicasets.apps") {
			t.Fatal("v0.10.2 unexpectedly uses strict v0.10.3 ReplicaSet proof")
		}
	}
}

func TestKagentCreateKeepsHistoricalPodListShapeContract(t *testing.T) {
	for _, scenario := range []string{"pods-missing-items", "pods-null-list", "pods-null-items", "pods-empty"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, scenario)
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), "Deployment has 0 proven ready pods, want 1") {
				t.Fatalf("v0.10.2 pod-list refusal changed: %v", err)
			}
			assertKagentNoClusterWrites(t, dir)
			for _, call := range kagentCalls(t, dir) {
				if slices.Contains(call.Args, "replicasets.apps") {
					t.Fatal("v0.10.2 unexpectedly read ReplicaSets")
				}
			}
		})
	}
}

func TestKagentLiftFingerprintsCompleteGeneratedDeploymentSpec(t *testing.T) {
	a, opt, _ := kagentLiftFixture(t, "")
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(opt.BundleDir, kagentLiftReceiptDir, kagentLiftReceiptName(opt.ToContext, "agents", "cluster-uid")))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleLiftReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Receipt.Workload) != 4 || len(receipt.Receipt.Workload[0].SpecDigest) != 64 {
		t.Fatalf("complete Deployment spec fingerprint missing: %+v", receipt.Receipt.Workload)
	}
}

func TestKagentUnsupportedDiscoveryAndChatAreTyped(t *testing.T) {
	adapter := kagentRuntimeAdapter{}
	_, probeErr := adapter.Probe(t.Context(), agentruntime.Target{})
	_, openErr := adapter.Open(t.Context(), agentruntime.Target{})
	for _, err := range []error{probeErr, openErr} {
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) {
			t.Fatalf("unsupported lifecycle boundary not typed: %v", err)
		}
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
			for _, call := range kagentCalls(t, dir)[before:] {
				if call.Body != nil {
					t.Fatal("config digest status/reuse attempted mutation")
				}
			}
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

func TestKagentCreateRejectsKubeletConfigDigestStatusImage(t *testing.T) {
	a, opt, dirOut, _, dir := kagentCreateFixture(t, "status-image-config")
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "published v0.10.2 digest") {
		t.Fatalf("v0.10.2 status image identity check changed: %v", err)
	}
	assertKagentNoClusterWrites(t, dir)
	if dirOut.Len() != 0 {
		t.Fatal("v0.10.2 refused image evidence emitted artifact")
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
			want := "KAIMAHI_CONFIRM=kind-test kmx --context kind-test agent lift " + opt.BundleDir + " --runtime kagent --runtime-version v0.10.3 --to-context kind-test --to-namespace agents --bindings '" + opt.Bindings + "'"
			if plan {
				want += " --plan"
			}
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "agent create") {
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
	a.InvocationCommand = "kmx agent create sample --task " + opt.Task
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "KAIMAHI_CONFIRM=kind-test kmx --context kind-test agent create sample --runtime kagent") {
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
	var writes []string
	for _, call := range kagentCalls(t, dir) {
		if call.Body["kind"] != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes = append(writes, fmtKind(call.Body))
		}
	}
	if !reflect.DeepEqual(writes, []string{"ModelConfig", "Agent"}) {
		t.Fatalf("actual writes = %v", writes)
	}
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
	if bytes.Contains(raw, []byte("secret-hash")) || bytes.Contains(raw, []byte("Answer briefly")) {
		t.Fatal("receipt leaked secret hash or instructions")
	}
	before := len(kagentCalls(t, dir))
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	for _, call := range kagentCalls(t, dir)[before:] {
		if call.Body != nil {
			t.Fatalf("repeat attempted mutation/admission: %+v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("kagent wrote Orka state: %v", err)
	}
	status := BundleStatusOptions{BundleDir: opt.BundleDir, Runtime: opt.Runtime, RuntimeVersion: opt.RuntimeVersion, Context: opt.ToContext, Output: "json"}
	a.Out.(*bytes.Buffer).Reset()
	if err := a.BundleStatus(status); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Targets []struct {
			State                                   string
			Ready                                   bool
			AgentUID, ModelConfigUID, DeploymentUID string
		}
	}
	if json.Unmarshal(a.Out.(*bytes.Buffer).Bytes(), &report) != nil || len(report.Targets) != 1 || report.Targets[0].State != "ready" || !report.Targets[0].Ready || report.Targets[0].AgentUID != "agent-uid" || report.Targets[0].DeploymentUID != "agent-deployment-uid" {
		t.Fatalf("status=%s", a.Out.(*bytes.Buffer).Bytes())
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
	for _, call := range kagentCalls(t, dir)[before:] {
		if call.Body != nil {
			t.Fatal("unbound inspection attempted writes")
		}
	}
}

func TestKagentLiftStatusDetectsDriftAndPending(t *testing.T) {
	for _, tc := range []struct {
		name, scenario, state string
		change                func(*testing.T, string)
	}{
		{name: "spec", scenario: "later-live-mutated", state: "drift"},
		{name: "secret replacement", scenario: "dependency-drift", state: "drift"},
		{name: "secret missing key", scenario: "dependency-missing-key", state: "drift"},
		{name: "stale generation", scenario: "stale-agent", state: "failed"},
		{name: "replacement", state: "drift", change: func(t *testing.T, dir string) {
			path := filepath.Join(dir, "sample-agents.kagent.dev.json")
			raw, _ := os.ReadFile(path)
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			obj["metadata"].(map[string]any)["uid"] = "replacement-agent-uid"
			raw, _ = json.Marshal(obj)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "partial", state: "drift", change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "sample-modelconfigs.kagent.dev.json")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir := kagentLiftFixture(t, "")
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, dir)
			}
			t.Setenv("KMX_KAGENT_TEST_SCENARIO", tc.scenario)
			before := len(kagentCalls(t, dir))
			got := readKagentStatusForTest(t, a, opt)
			if got.State != tc.state || got.Ready {
				t.Fatalf("status=%+v", got)
			}
			if err := a.LiftAgentBundle(opt); err == nil {
				t.Fatal("drift/pending authorized repeat")
			}
			for _, call := range kagentCalls(t, dir)[before:] {
				if call.Body != nil {
					t.Fatal("repeat/status wrote or dry-ran changed deployment")
				}
			}
		})
	}
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
				entries, _ := os.ReadDir(filepath.Join(opt.BundleDir, kagentLiftReceiptDir))
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".json") {
						t.Fatal("MCP drift wrote success receipt")
					}
				}
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
			for _, call := range kagentCalls(t, dir)[before:] {
				if call.Body != nil {
					t.Fatal("MCP receipt-bound repeat wrote")
				}
			}
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

func TestKagentCoreOnlyBundleRendersWithoutDroppedBehavior(t *testing.T) {
	a, opt, _ := kagentLiftFixture(t, "")
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	raw, _ := os.ReadFile(path)
	index := bytes.Index(raw, []byte("extensions:"))
	if index < 0 {
		t.Fatal("fixture has no extension")
	}
	raw = raw[:index]
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, _, rendered, _, err := a.prepareKagentBundle(opt.BundleDir, "", opt.ToContext, "")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.PortableDigest() != agentruntime.PortableBundleDigest(raw) || len(rendered.Documents()) != 3 || len(rendered.DeployDocuments()) != 2 {
		t.Fatal("render/digest or review-only Secret contract changed")
	}
	bundle, err := kagentBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"] != "go" {
		t.Fatal("core default is not go")
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
	for _, path := range []string{kagentReceiptPath, foreignPath} {
		after, err := os.ReadFile(path)
		want := kagentReceipt
		if path == foreignPath {
			want = foreignBefore
		}
		if err != nil || !bytes.Equal(after, want) {
			t.Fatalf("Orka workflow consumed foreign receipt %s: %v", path, err)
		}
	}
	useKagent()
	if got := readKagentStatusForTest(t, kagent, kagentOpt); got.State != "ready" {
		t.Fatalf("Orka retirement changed Kagent placement: %+v", got)
	}
}

func TestOrkaReceiptReadersIgnoreKagentAndRetainHistoricalReceipts(t *testing.T) {
	bundle := t.TempDir()
	for _, runtimeName := range []agentruntime.ID{"", agentruntime.Orka, agentruntime.Kagent} {
		if err := writeLiftReceipt(bundle, "cluster-"+string(runtimeName), bundleLiftReceipt{Receipt: agentruntime.DeployReceipt{Target: agentruntime.DeployTarget{Runtime: runtimeName, Context: "kind-test", Namespace: "agents"}}}); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := loadBundleReceiptTargets(bundle)
	if err != nil || len(targets) != 2 {
		t.Fatalf("Orka mixed history=%v err=%v", targets, err)
	}
	path := bundleReceiptPath(bundle, "kind-test", "agents", "cluster-kagent")
	if _, ok := readBundleReceiptAt(path); ok {
		t.Fatal("Orka retirement accepted kagent receipt")
	}
	for _, runtimeName := range []string{"", "orka"} {
		if _, ok := readBundleReceiptAt(bundleReceiptPath(bundle, "kind-test", "agents", "cluster-"+runtimeName)); !ok {
			t.Fatal("historical/native Orka receipt lost")
		}
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

func TestKagentLiftDoesNotCreateAgentAfterModelRejection(t *testing.T) {
	a, opt, dir := kagentLiftFixture(t, "model-loses-acceptance")
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "Accepted") {
		t.Fatalf("ModelConfig rejection=%v", err)
	}
	var writes []string
	for _, call := range kagentCalls(t, dir) {
		if call.Body["kind"] != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes = append(writes, fmtKind(call.Body))
		}
	}
	if !reflect.DeepEqual(writes, []string{"ModelConfig"}) {
		t.Fatalf("model rejection did not stop Agent create: %v", writes)
	}
}

func TestKagentLiftFailClosedWritesNoReceipt(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
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
			entries, _ := os.ReadDir(filepath.Join(opt.BundleDir, "receipts", "kagent-v0.10.3"))
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") {
					t.Fatalf("failed lift wrote receipt %s", entry.Name())
				}
			}
			if tc.scenario == "unknown-create" {
				writes := 0
				for _, call := range kagentCalls(t, dir) {
					if call.Body != nil && !slices.Contains(call.Args, "--dry-run=server") {
						writes++
					}
				}
				if writes != 1 {
					t.Fatalf("unknown create was retried: %d writes", writes)
				}
			}
		})
	}
}

// Dropping native behavior must remain a typed refusal, not a lossy render.
func TestKagentLiftBehaviorRefusalIsTyped(t *testing.T) {
	source := []byte(`apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: sample
spec:
  description: Sample
  instructions: Answer briefly.
  model:
    name: gpt-4o-mini
  coordination:
    allowedAgents:
      - name: helper
`)
	_, err := agentruntime.PreparePortableRender(source, kagentRuntimeAdapter{})
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) || unsupported.Verb != "render" || !strings.Contains(err.Error(), "spec.coordination.allowedAgents") {
		t.Fatalf("behavior refusal must wrap typed render refusal and field path: %v", err)
	}
}
