package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func liftBundleFixture(t *testing.T) (*App, LiftAgentBundleOptions, string, *bytes.Buffer) {
	t.Helper()
	adapter, _, dir := reconcileFixture(t)
	t.Setenv("KMX_LIFT_TEST", "1")
	t.Setenv("KMX_HOME", filepath.Join(dir, "user-state"))
	source, err := portableOrkaSource(*adapter.create)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "portable")
	if err := writeOrkaBundle(bundle, source, mustBindingsSource(t, *adapter.create)); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	adapter.app.Err = &notes
	return adapter.app, LiftAgentBundleOptions{BundleDir: bundle, ToContext: "kind-test", Inference: "provider:inference"}, dir, &notes
}

func assertNoLiftWrites(t *testing.T, dir, bundle string) {
	t.Helper()
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("lift wrote resource: %+v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(bundle, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("lift wrote receipts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "user-state", "bundle-lift")); !os.IsNotExist(err) {
		t.Fatalf("lift wrote preferences: %v", err)
	}
}

func TestBundleLiftSelectionPathUsesKMXHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := bundleLiftSelectionPath(filepath.Join(t.TempDir(), "agents", "sample"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(home, "bundle-lift") {
		t.Fatalf("selection path = %q, want it under KMX_HOME", path)
	}
}

// Catches plans that write state/receipts/resources, render creation bindings,
// or inspect the ambient context instead of the explicitly selected context.
func TestLiftBundlePlanIsReadOnlyAndMapsTargetProvider(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	a.Cfg.KubeContext = "unrelated-ambient-context"
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
	if !strings.Contains(notes.String(), "kind-test") || !strings.Contains(notes.String(), "orka-system") || !strings.Contains(notes.String(), "created") {
		t.Fatalf("plan omitted target and outcomes: %s", notes.String())
	}
	calls := orkaCalls(t, dir)
	for _, call := range calls {
		if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
			t.Fatalf("ambient context used: %+v", call)
		}
		if call.Document == nil || call.Document["kind"] != "Provider" {
			continue
		}
		spec := call.Document["spec"].(map[string]any)
		if spec["baseURL"] != "https://target.example.invalid/v1" || spec["type"] != "openai" || spec["secretRef"].(map[string]any)["name"] != "target-secret" || spec["defaultModel"] != "gpt-4o-mini" {
			t.Fatalf("target mapping or portable model lost: %+v", spec)
		}
	}
}

func TestLiftBundlePlanQueriesLabelSelectedChartController(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	t.Setenv("KMX_LIFT_CONTROLLER_NAME", "w112-controller")
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	var discovered, rolled bool
	for _, call := range orkaCalls(t, dir) {
		joined := strings.Join(call.Args, " ")
		if strings.Contains(joined, "get deploy -o json") {
			discovered = true
		}
		if strings.Contains(joined, "rollout status deploy/w112-controller --timeout=10s") {
			rolled = true
		}
	}
	if !discovered || !rolled {
		t.Fatalf("did not query and roll out the label-selected chart controller: discovered=%t rolled=%t", discovered, rolled)
	}
}

// A comment-only portable change leaves rendered fields unchanged, but lift
// still refreshes both ownership digests. Plan must say that it will write.
func TestLiftBundlePlanReportsMarkerRefresh(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte("\n# comment-only revision\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	notes.Reset()
	before := len(orkaCalls(t, dir))
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if got := notes.String(); strings.Count(got, "reused; ownership markers would be refreshed") != 2 {
		t.Fatalf("plan hid two planned marker writes: %s", got)
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if slices.Contains(call.Args, "patch") || call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("plan wrote resource: %+v", call)
		}
	}
}

// A selected inference Provider with the rendered Agent's name must never be
// adopted, replaced, or reused as the newly rendered Provider.
func TestLiftBundleReportsIndependentOrkaAndNamespaceGaps(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	t.Setenv("KMX_LIFT_MISSING", "crd,controller,namespace")
	err := a.LiftAgentBundle(opt)
	if err == nil {
		t.Fatal("missing prerequisites were accepted")
	}
	for _, want := range []string{"CRDs", "controller", "namespace", "kmx orka install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s refusal: %v", want, err)
		}
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestLiftBundleRefusesInferenceProviderNameCollision(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	opt.Inference = "provider:sample"
	t.Setenv("KMX_LIFT_SELECTED_SAME", "1")
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "selected Provider") {
		t.Fatalf("selected Provider name collision not refused: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func liftBundleWithCoordination(t *testing.T, path string, names ...string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	orka := doc["extensions"].(map[string]any)["orka"].(map[string]any)
	agent := orka["agent"].(map[string]any)
	refs := make([]any, 0, len(names))
	for _, name := range names {
		refs = append(refs, map[string]any{"name": name})
	}
	agent["coordination"] = map[string]any{"enabled": true, "allowedAgents": refs, "maxDepth": 2}
	encoded, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "agent.yaml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLiftPlanSkipsDisabledAndSelfAllowedAgentPrerequisites(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		disabled    bool
	}{
		{"disabled coordination", "helper", true},
		{"self delegation", "sample", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			liftBundleWithCoordination(t, opt.BundleDir, tc.agent)
			if tc.disabled {
				path := filepath.Join(opt.BundleDir, "agent.yaml")
				source, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				updated := bytes.Replace(source, []byte("enabled: true"), []byte("enabled: false"), 1)
				if bytes.Equal(source, updated) {
					t.Fatal("fixture missing enabled flag")
				}
				if err := os.WriteFile(path, updated, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("KMX_LIFT_MISSING", "allowed-agent")
			opt.Plan = true
			if err := a.LiftAgentBundle(opt); err != nil {
				t.Fatalf("irrelevant Agent prerequisite refused: %v", err)
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
		})
	}
}

func TestLiftPlanRefusesMissingAllowedAgentsAndUnsupportedCRD(t *testing.T) {
	for _, tc := range []struct{ name, missing, want string }{
		{"missing helpers", "allowed-agent", "helper, other"},
		{"API failure", "allowed-agent-read", "cannot inspect Agent/helper"},
		{"unsupported installed Agent schema", "coordination-crd", "spec.coordination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			liftBundleWithCoordination(t, opt.BundleDir, "helper", "other")
			t.Setenv("KMX_LIFT_MISSING", tc.missing)
			opt.Plan = true
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("plan accepted missing prerequisite: %v", err)
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
		})
	}
}

func TestLiftBundleEveryPrerequisiteRefusesWithoutWrites(t *testing.T) {
	for _, tc := range []struct{ gap, want string }{
		{"crd", "kmx orka install"}, {"controller", "kmx orka install"}, {"namespace", "namespace"},
		{"provider", "Provider"}, {"provider-ready", "Ready"}, {"provider-key", "Secret reference"}, {"secret", "Secret"},
		{"tool", "Tool/search"}, {"tool-available", "Available"},
	} {
		t.Run(tc.gap, func(t *testing.T) {
			a, opt, dir, _ := liftBundleFixture(t)
			t.Setenv("KMX_LIFT_MISSING", tc.gap)
			err := a.LiftAgentBundle(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("gap %s: %v", tc.gap, err)
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
		})
	}
}

// The first successful deployment saves a private bundle-scoped selection;
// missing flags reuse it, but a changed kube-system UID must refuse even if
// kubeconfig still calls the context by the same name.
// A clean committed agent.yaml is identified by HEAD even when unrelated
// untracked files exist; editing the portable file alone makes it uncommitted.
func TestLiftBundleReceiptGitCommitTracksOnlyPortableRevision(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	originalPATH := os.Getenv("PATH")
	a, opt, dir, _ := liftBundleFixture(t)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+originalPATH)
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.Command(git, append([]string{"-C", dir}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init", "--quiet")
	runGit("add", "portable/agent.yaml")
	runGit("commit", "--quiet", "-m", "agent")
	commit := runGit("rev-parse", "HEAD")
	if got := liftAgentCommit(context.Background(), opt.BundleDir, agentruntime.PortableBundleDigest([]byte("stale rendered revision"))); got != "uncommitted" {
		t.Fatalf("Git provenance described a different rendered revision: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.tmp"), []byte("not portable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(files) != 1 {
		t.Fatalf("missing receipt: %v: %v", files, err)
	}
	path := filepath.Join(opt.BundleDir, "receipts", files[0].Name())
	var wrapper bundleLiftReceipt
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.GitCommit != commit {
		t.Fatalf("unrelated untracked file tainted clean agent: %q vs %q", wrapper.GitCommit, commit)
	}
	original, err := os.ReadFile(filepath.Join(opt.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.BundleDir, "agent.yaml"), append(slices.Clone(original), []byte("\n# staged revision\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "portable/agent.yaml")
	if err := os.WriteFile(filepath.Join(opt.BundleDir, "agent.yaml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if got := liftAgentCommit(context.Background(), opt.BundleDir, wrapper.Receipt.PortableDigest); got != "uncommitted" {
		t.Fatalf("staged but uncommitted revision reported HEAD: %q", got)
	}
	runGit("reset", "--quiet", "HEAD", "--", "portable/agent.yaml")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := liftAgentCommit(cancelled, opt.BundleDir, wrapper.Receipt.PortableDigest); got != "uncommitted" {
		t.Fatalf("cancelled provenance check returned a commit: %q", got)
	}
	before := wrapper.Receipt.PortableDigest
	agentPath := filepath.Join(opt.BundleDir, "agent.yaml")
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, append(agent, []byte("\n# revised instructions\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	// Git's skip-worktree hint can hide modified bytes from `git diff`; the
	// receipt must compare the actual portable file to HEAD instead.
	runGit("update-index", "--skip-worktree", "portable/agent.yaml")
	var editedNotes bytes.Buffer
	a.Err = &editedNotes
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(editedNotes.String(), "working file differs from HEAD") {
		t.Fatalf("warning did not identify edited file: %s", editedNotes.String())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.GitCommit != "uncommitted" || wrapper.Receipt.PortableDigest == before {
		t.Fatalf("edited agent was treated as same clean revision: %+v", wrapper)
	}
}

func TestLiftBundleSavesReceiptAndRefusesStaleRememberedCluster(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("expected one receipt per target: %v, %v", receipts, err)
	}
	raw, err := os.ReadFile(filepath.Join(opt.BundleDir, "receipts", receipts[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var wrapper struct {
		GitCommit string                     `json:"gitCommit"`
		Receipt   agentruntime.DeployReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.GitCommit != "uncommitted" || wrapper.Receipt.Target.Context != "kind-test" || wrapper.Receipt.PortableDigest == "" || wrapper.Receipt.Resources[1].Outcome != agentruntime.ResourceCreated {
		t.Fatalf("invalid receipt: %+v", wrapper)
	}
	source, err := os.ReadFile(filepath.Join(opt.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.Receipt.PortableDigest != agentruntime.PortableBundleDigest(source) {
		t.Fatal("receipt directory changed digest of exact agent.yaml bytes")
	}
	if strings.Count(notes.String(), "uncommitted") != 1 || !strings.Contains(notes.String(), "not in a Git repository") {
		t.Fatalf("git warning missing, misleading or duplicated: %s", notes.String())
	}
	firstDigest := wrapper.Receipt.PortableDigest
	opt.ToContext, opt.Inference = "", ""
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatalf("remembered target rejected: %v", err)
	}
	raw, err = os.ReadFile(filepath.Join(opt.BundleDir, "receipts", receipts[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.Receipt.PortableDigest != firstDigest || wrapper.Receipt.Resources[1].Outcome != agentruntime.ResourceReused {
		t.Fatalf("rerun not reused: %+v", wrapper)
	}
	t.Setenv("KMX_LIFT_CLUSTER_UID", "other-cluster-uid")
	statePath, err := bundleLiftSelectionPath(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), statePath) {
		t.Fatalf("context repointed to another cluster without naming state file to remove: %v", err)
	}
	opt.ToContext, opt.Inference = "kind-test", "provider:inference"
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("explicit flags bypassed stale remembered target: %v", err)
	}
	if len(orkaCalls(t, dir)) == 0 {
		t.Fatal("fake kubectl not exercised")
	}
}

func TestLiftBundleRemoteContextGuardRefusesUnconfirmedWrite(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	t.Setenv("KMX_RECONCILE_REMOTE", "1")
	if err := a.LiftAgentBundle(opt); err == nil {
		t.Fatal("remote target accepted without confirmation")
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestLiftBundleNamesPartialWritesOnDeployFailure(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	t.Setenv("KMX_RECONCILE_FAIL_ONCE", "Agent")
	err := a.LiftAgentBundle(opt)
	if err == nil || !strings.Contains(err.Error(), "may have been changed") {
		t.Fatalf("deployment failure hid partial resource changes: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "providers.core.orka.ai.json")); statErr != nil {
		t.Fatalf("fixture did not exercise a partial Provider write: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(opt.BundleDir, "receipts")); !os.IsNotExist(statErr) {
		t.Fatalf("failed deployment wrote receipt: %v", statErr)
	}
}

func TestLiftCoordinationDepthUpdateIsPlannedAndDeployed(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	liftBundleWithCoordination(t, opt.BundleDir, "helper")
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(source, []byte("maxDepth: 2"), []byte("maxDepth: 3"), 1)
	if bytes.Equal(source, changed) {
		t.Fatal("fixture missing maxDepth")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	notes.Reset()
	before := len(orkaCalls(t, dir))
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "Agent/sample: updated") || !strings.Contains(notes.String(), "spec.coordination.maxDepth") {
		t.Fatalf("missing depth update path: %s", notes.String())
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("plan wrote update: %+v", call)
		}
	}
	opt.Plan = false
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var live map[string]any
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	coord := live["spec"].(map[string]any)["coordination"].(map[string]any)
	if coord["maxDepth"] != float64(3) {
		t.Fatalf("deployed maxDepth = %v", coord["maxDepth"])
	}
}

func TestLiftBundlePlanPredictsInstructionsUpdateWithoutWriting(t *testing.T) {
	a, opt, dir, notes := liftBundleFixture(t)
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(source, []byte("Answer briefly"), []byte("Answer carefully"), 1)
	if bytes.Equal(source, changed) {
		t.Fatal("fixture missing original instructions")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))
	notes.Reset()
	opt.Plan = true
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "Agent/sample: updated") || !strings.Contains(notes.String(), "spec.systemPrompt.inline") {
		t.Fatalf("missing safe update diff: %s", notes.String())
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("plan wrote update: %+v", call)
		}
	}
	opt.Plan = false
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("missing one target receipt: %v %v", receipts, err)
	}
	data, err := os.ReadFile(filepath.Join(opt.BundleDir, "receipts", receipts[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var wrapper bundleLiftReceipt
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.Receipt.Resources[1].Outcome != agentruntime.ResourceUpdated {
		t.Fatalf("plan/deploy disagreed about instructions: %+v", wrapper.Receipt.Resources)
	}
}

func TestLiftBundleRejectsReceiptsSymlinkBeforeResourceWrites(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	destination := t.TempDir()
	if err := os.Symlink(destination, filepath.Join(opt.BundleDir, "receipts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "receipts") {
		t.Fatalf("unsafe receipts destination: %v", err)
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("deployed before refusing symlink: %+v", call)
		}
	}
}

func TestLiftBundlePlanRefusesUnsafeReceiptLocation(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	opt.Plan = true
	if err := os.Symlink(t.TempDir(), filepath.Join(opt.BundleDir, "receipts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "receipts") {
		t.Fatalf("plan predicted deployable bundle with unsafe receipt location: %v", err)
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("plan wrote a resource: %+v", call)
		}
	}
}

func TestLiftBundlePlanAfterDeployDoesNotChangeReceiptOrState(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("missing receipt: %v, %v", receipts, err)
	}
	receiptPath := filepath.Join(opt.BundleDir, "receipts", receipts[0].Name())
	before, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := bundleLiftSelectionPath(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(orkaCalls(t, dir))
	opt.Plan, opt.ToContext, opt.Inference = true, "", ""
	if err := a.LiftAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(selection, current) {
		t.Fatal("plan changed receipt or state")
	}
	for _, call := range orkaCalls(t, dir)[calls:] {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("plan wrote a resource: %+v", call)
		}
	}
}
