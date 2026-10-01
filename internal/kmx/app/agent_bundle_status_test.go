package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// bundleStatusFixture wires the same fake kubectl reconcileFixture uses,
// seeds a matching kube-system UID for the "kind-test" context (liftClusterUID
// is now read for every target), and writes a portable bundle from the
// golden create. It returns the App, a base BundleStatusOptions naming that
// bundle, the shared fixture directory, the exact Render this create
// produces, and the bundle's own name.
func bundleStatusFixture(t *testing.T) (*App, BundleStatusOptions, string, agentruntime.RenderedBundle, string) {
	t.Helper()
	adapter, rendered, dir := reconcileFixture(t)
	t.Setenv("KMX_HOME", filepath.Join(dir, "user-state"))
	seedBundleNamespace(t, dir, "cluster-uid")
	source, err := portableOrkaSource(*adapter.create)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "portable")
	if err := writeOrkaBundle(bundle, source, mustBindingsSource(t, *adapter.create)); err != nil {
		t.Fatal(err)
	}
	return adapter.app, BundleStatusOptions{BundleDir: bundle}, dir, rendered, adapter.create.Name
}

// seedBundleNamespace writes the Namespace object liftClusterUID reads
// through the fake's generic (non-KMX_LIFT_TEST) path.
func seedBundleNamespace(t *testing.T, dir, uid string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": uid}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "namespace.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func markBundleOwned(doc map[string]any, name string, rendered agentruntime.RenderedBundle) {
	meta := doc["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations[orkaBundleMarker] = name
	annotations[orkaPortableMarker] = rendered.PortableDigest()
	annotations[orkaRenderedMarker] = rendered.RenderedDigest()
	meta["annotations"] = annotations
}

func seedBundleLiveResources(t *testing.T, dir string, rendered agentruntime.RenderedBundle, name string, edit func(agent map[string]any)) {
	t.Helper()
	provider := reconcileLive(t, dir, "Provider", rendered)
	markBundleOwned(provider, name, rendered)
	seedReconcile(t, dir, provider)
	agent := reconcileLive(t, dir, "Agent", rendered)
	markBundleOwned(agent, name, rendered)
	if edit != nil {
		edit(agent)
	}
	seedReconcile(t, dir, agent)
}

// writeBundleReceipt writes a receipt at the exact filename the real lift
// path would use for this context, namespace and cluster UID, so the
// identity check (recomputed from the same formula) can find it.
func writeBundleReceipt(t *testing.T, bundle, context, namespace, clusterUID, name, gitCommit string) {
	t.Helper()
	if err := writeLiftReceipt(bundle, clusterUID, bundleLiftReceipt{
		Receipt:   agentruntime.DeployReceipt{Bundle: name, Target: agentruntime.DeployTarget{Context: context, Namespace: namespace}},
		GitCommit: gitCommit,
	}); err != nil {
		t.Fatal(err)
	}
}

// Explicit --context with no receipt still reads live: the observed cluster
// UID is shown, and the report notes the absence of local history rather
// than pretending one exists.
func TestBundleStatusExplicitContextWithoutReceiptShowsClusterUIDAndNoReceipt(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	opt.Context = "kind-test"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 {
		t.Fatalf("expected exactly one target, got %+v", report.Targets)
	}
	target := report.Targets[0]
	if target.Recorded || !target.NoReceipt {
		t.Fatalf("explicit unrecorded context should note no receipt: %+v", target)
	}
	if target.ObservedClusterUID != "cluster-uid" {
		t.Fatalf("expected the actual observed cluster UID to be shown: %+v", target)
	}
	if target.State != bundleStateNotDeployed {
		t.Fatalf("expected not deployed, got %+v", target)
	}
}

// A target this bundle was never lifted to, and has no live resources, is
// simply not deployed — not an error, and not confused with drift.
func TestBundleStatusReportsNotDeployedForFreshTarget(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateNotDeployed {
		t.Fatalf("expected a single not-deployed target: %+v", report.Targets)
	}
}

// A live Agent and Provider that exactly match today's agent.yaml, and carry
// a valid ownership marker for both, report in sync — with UID, generation
// and Ready read from both live objects.
func TestBundleStatusReportsInSyncForMatchingLiveResources(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 {
		t.Fatalf("expected one target: %+v", report.Targets)
	}
	target := report.Targets[0]
	if target.State != bundleStateInSync {
		t.Fatalf("expected in sync, got %+v", target)
	}
	if !target.Agent.Ready || !target.Provider.Ready {
		t.Fatalf("expected Ready to be read for both Provider and Agent: %+v", target)
	}
	if target.Agent.UID == "" || target.Agent.Generation == 0 || target.Provider.UID == "" || target.Provider.Generation == 0 {
		t.Fatalf("expected UID and generation to be read for both Provider and Agent: %+v", target)
	}
	if !target.Agent.Marked || !target.Provider.Marked || target.Agent.MarkerOwner != name || target.Provider.MarkerOwner != name {
		t.Fatalf("expected both markers to be read and owned by this bundle: %+v", target)
	}
	if len(target.ChangedFields) != 0 {
		t.Fatalf("in-sync target must carry no changed fields: %+v", target)
	}
}

// A live Agent whose systemPrompt was edited directly on the cluster, while
// its ownership marker still names the current revision, reports drifted —
// with the exact changed field path planOrkaReconcile itself computes, the
// same comparison `kmx agent lift --plan` uses.
func TestBundleStatusReportsDriftedWithSharedChangedFieldPaths(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, func(agent map[string]any) {
		agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "edited live"}
	})
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.State != bundleStateDrifted {
		t.Fatalf("expected drifted, got %+v", target)
	}
	found := false
	for _, field := range target.ChangedFields {
		if field == "Agent.spec.systemPrompt.inline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing expected changed field: %+v", target.ChangedFields)
	}
}

// A cluster-side edit with unchanged ownership digest is reported as drift.
func TestBundleStatusNamesCoordinationDepthDrift(t *testing.T) {
	a, opt, dir, _, name := bundleStatusFixture(t)
	liftBundleWithCoordination(t, opt.BundleDir, "helper")
	source, err := os.ReadFile(filepath.Join(opt.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	create := goldenNoTaskCreate("")
	adapter := lifecycleAdapter(t, create)
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seedBundleLiveResources(t, dir, rendered, name, func(agent map[string]any) {
		agent["spec"].(map[string]any)["coordination"].(map[string]any)["maxDepth"] = 4
	})
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.State != bundleStateDrifted || !slices.Contains(target.ChangedFields, "Agent.spec.coordination.maxDepth") {
		t.Fatalf("coordination hand edit not reported: %+v", target)
	}
}

// A live Agent marked as belonging to a different bundle is refused as
// belonging to another bundle, never silently compared or adopted.
func TestBundleStatusReportsBelongsToAnotherBundle(t *testing.T) {
	a, opt, dir, rendered, _ := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, "someone-else", nil)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].State != bundleStateForeign {
		t.Fatalf("expected belongs to another bundle, got %+v", report.Targets[0])
	}
}

// A live Agent found with no ownership marker at all is unknown, not
// silently adopted or compared: status never claims ownership the way lift's
// own reconcile may.
func TestBundleStatusReportsUnknownForMissingMarker(t *testing.T) {
	a, opt, dir, rendered, _ := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	seedReconcile(t, dir, provider)
	agent := reconcileLive(t, dir, "Agent", rendered)
	seedReconcile(t, dir, agent) // no markBundleOwned: no ownership marker at all
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.State != bundleStateUnknown || !strings.Contains(target.Detail, "missing ownership marker") {
		t.Fatalf("expected unknown with a missing-marker detail, got %+v", target)
	}
}

// A Provider written without its Agent (a partial deploy failure, or manual
// deletion) is unknown: distinct from both not-deployed and drifted, and
// never guessed at.
func TestBundleStatusReportsUnknownForPartialResources(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	markBundleOwned(provider, name, rendered)
	seedReconcile(t, dir, provider)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].State != bundleStateUnknown {
		t.Fatalf("expected unknown, got %+v", report.Targets[0])
	}
}

// A context this fake kubectl refuses (anything but "kind-test") is reported
// unknown, with no raw process output leaked into Detail, and the command
// itself still succeeds: a target observation failure is never a command
// failure.
func TestBundleStatusUnreachableTargetDoesNotFailOrLeakStderr(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	opt.Context, opt.Namespace = "unreachable-context", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatalf("a target observation failure must not fail the report: %v", err)
	}
	target := report.Targets[0]
	if target.State != bundleStateUnknown {
		t.Fatalf("expected unknown, got %+v", target)
	}
	if strings.Contains(target.Detail, "private-token-must-not-escape") || strings.Contains(target.Detail, "token") {
		t.Fatalf("raw process output leaked into status: %q", target.Detail)
	}
	// The same guarantee holds through the public entry point.
	if err := a.BundleStatus(opt); err != nil {
		t.Fatalf("BundleStatus must exit zero for a target observation failure: %v", err)
	}
}

// A recorded target whose cluster identity no longer matches the receipt
// filename's encoded UID is reported target changed, not silently compared
// against the wrong cluster. This is checked from the receipt itself, with
// no remembered selection involved at all.
func TestBundleStatusRefusesTargetChangedFromReceiptFilenameAlone(t *testing.T) {
	a, opt, dir, _, name := bundleStatusFixture(t)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	opt.Context, opt.Namespace = "kind-test", "orka-system"

	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateNotDeployed {
		t.Fatalf("expected the recorded target to be read as not deployed while cluster identity matches: %+v", report.Targets)
	}

	seedBundleNamespace(t, dir, "a-different-cluster")
	report, err = a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateChanged {
		t.Fatalf("expected target changed once cluster identity no longer matches the receipt filename: %+v", report.Targets)
	}
	if report.Targets[0].ObservedClusterUID != "a-different-cluster" {
		t.Fatalf("expected the actual observed cluster UID to still be shown: %+v", report.Targets[0])
	}
}

// With no --context, the remembered selection is the fallback target only
// when no receipt exists; once receipts exist, every one of them is
// reported, each with its own state.
func TestBundleStatusListsEveryReceiptTargetWithItsOwnState(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "other-namespace", "cluster-uid", name, "uncommitted")

	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 2 {
		t.Fatalf("expected both receipt targets: %+v", report.Targets)
	}
	byNamespace := map[string]bundleTargetStatus{}
	for _, target := range report.Targets {
		if !target.Recorded {
			t.Fatalf("receipt targets must be recorded: %+v", target)
		}
		byNamespace[target.Namespace] = target
	}
	if byNamespace["orka-system"].State != bundleStateInSync {
		t.Fatalf("expected the matching namespace to be in sync: %+v", byNamespace["orka-system"])
	}
	// The live objects are seeded under orka-system; a receipt naming a
	// different namespace observes the same live objects under a namespace
	// that does not match their own metadata, which planOrkaReconcile
	// refuses rather than silently comparing across namespaces.
	if byNamespace["other-namespace"].State == bundleStateInSync {
		t.Fatalf("a namespace mismatch must not read as in sync: %+v", byNamespace["other-namespace"])
	}
	_ = dir
}

// An unreadable or invalid bundle is the one case that fails the command
// outright, since there is nothing to report on at all.
func TestBundleStatusRefusesUnreadableOrInvalidBundle(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	if err := os.Remove(filepath.Join(opt.BundleDir, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.bundleStatusReport(opt); err == nil {
		t.Fatal("missing agent.yaml was accepted")
	}
	if err := a.BundleStatus(opt); err == nil {
		t.Fatal("BundleStatus accepted a bundle with no agent.yaml")
	}
}

func TestBundleStatusRejectsUnsupportedOutput(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	opt.Context, opt.Output = "kind-test", "yaml"
	if err := a.BundleStatus(opt); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected an output format refusal, got %v", err)
	}
}

// TestBundleStatusPrintsTableAndJSON exercises the public entry point end to
// end: table output names the target and its state; JSON output round-trips
// the same facts through the public shape.
func TestBundleStatusPrintsTableAndJSON(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	opt.Context, opt.Namespace = "kind-test", "orka-system"

	var table strings.Builder
	a.Out = &table
	if err := a.BundleStatus(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "kind-test") || !strings.Contains(table.String(), bundleStateInSync) {
		t.Fatalf("table output missing target or state: %s", table.String())
	}

	var body strings.Builder
	a.Out = &body
	opt.Output = "json"
	if err := a.BundleStatus(opt); err != nil {
		t.Fatal(err)
	}
	var decoded bundleStatusReport
	if err := json.Unmarshal([]byte(body.String()), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v: %s", err, body.String())
	}
	if len(decoded.Targets) != 1 || decoded.Targets[0].State != bundleStateInSync {
		t.Fatalf("JSON output did not round-trip the report: %+v", decoded)
	}
	if !decoded.Targets[0].Provider.Ready || !decoded.Targets[0].Agent.Ready {
		t.Fatalf("JSON output dropped Provider/Agent readiness: %+v", decoded.Targets[0])
	}
}

// runBundleStatusGit runs git against dir with hooks disabled: a global
// commit-msg or pre-commit hook on this machine may reference tools that are
// not on the fixture's fake-kubectl-only PATH, and this test's git history is
// scratch data these tests do not want any local hook policing.
func runBundleStatusGit(t *testing.T, git, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// A live digest that matches an older commit than HEAD, with no live field
// edits since, is reported behind — found within Git history bounded to
// agent.yaml's own revisions, independent of any receipt.
func TestBundleStatusReportsBehindFromLiveDigestAloneNoReceiptNeeded(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	originalPATH := os.Getenv("PATH")
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	// reconcileFixture pins PATH to the fake-kubectl-only directory; git needs
	// the real toolchain too.
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+originalPATH)
	runBundleStatusGit(t, git, opt.BundleDir, "init", "--quiet")
	runBundleStatusGit(t, git, opt.BundleDir, "add", "agent.yaml")
	runBundleStatusGit(t, git, opt.BundleDir, "commit", "--quiet", "-m", "deployed revision")
	fullCommit := runBundleStatusGit(t, git, opt.BundleDir, "rev-parse", "HEAD")

	// Seed the live objects as they were rendered from this exact commit — no
	// field drift, only the digest is now behind. No receipt is written at
	// all: behind must not depend on one.
	seedBundleLiveResources(t, dir, rendered, name, nil)

	// A newer commit changes agent.yaml, so the live digest (still annotated
	// with the deployed commit's content) no longer matches HEAD.
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte("\n# a later revision\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, opt.BundleDir, "add", "agent.yaml")
	runBundleStatusGit(t, git, opt.BundleDir, "commit", "--quiet", "-m", "later revision")

	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.State != bundleStateBehind {
		t.Fatalf("expected behind, got %+v", target)
	}
	if target.Behind != 1 {
		t.Fatalf("expected 1 commit behind, got %d: %+v", target.Behind, target)
	}
	if target.deployedCommitFull != fullCommit || target.DeployedCommit != fullCommit[:7] {
		t.Fatalf("status lost the full deployed revision: %+v", target)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), fullCommit) {
		t.Fatal("internal full SHA leaked into status JSON")
	}
}

// A live digest that matches nothing in bounded Git history is still
// reported behind, never unknown, with a detail explaining it could not be
// located.
func TestBundleStatusReportsBehindWhenDeployedRevisionNotFoundInGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	originalPATH := os.Getenv("PATH")
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+originalPATH)
	runBundleStatusGit(t, git, opt.BundleDir, "init", "--quiet")
	runBundleStatusGit(t, git, opt.BundleDir, "add", "agent.yaml")
	runBundleStatusGit(t, git, opt.BundleDir, "commit", "--quiet", "-m", "only revision")

	// Both Provider and Agent must agree on the deployed digest for the
	// comparison to proceed at all; a mismatch between the two is its own
	// unknown state, tested separately. Here both are set to a digest that
	// was never committed to this bundle, so the search cannot find it.
	neverCommitted := agentruntime.PortableBundleDigest([]byte("never committed to this bundle"))
	seedBundleLiveResources(t, dir, rendered, name, func(agent map[string]any) {
		meta := agent["metadata"].(map[string]any)
		annotations := meta["annotations"].(map[string]any)
		annotations[orkaPortableMarker] = neverCommitted
	})
	// Overwrite the Provider's annotation to the same value the Agent now
	// carries, seeded above via seedBundleLiveResources.
	providerRaw, err := os.ReadFile(filepath.Join(dir, "providers.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var providerDoc map[string]any
	if err := json.Unmarshal(providerRaw, &providerDoc); err != nil {
		t.Fatal(err)
	}
	providerDoc["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaPortableMarker] = neverCommitted
	seedReconcile(t, dir, providerDoc)

	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.State != bundleStateBehind {
		t.Fatalf("expected behind even when the deployed revision cannot be located, got %+v", target)
	}
	if target.Behind != 0 || !strings.Contains(target.Detail, "not found in Git") {
		t.Fatalf("expected a not-found-in-Git detail with no count, got %+v", target)
	}
}

// Both Provider and Agent expose their own portable and rendered digest
// markers in the report, not only the Agent's.
func TestBundleStatusExposesBothResourcesMarkerDigests(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	target := report.Targets[0]
	if target.Provider.PortableDigest == "" || target.Provider.RenderedDigest == "" {
		t.Fatalf("expected the Provider's own marker digests to be read: %+v", target.Provider)
	}
	if target.Agent.PortableDigest == "" || target.Agent.RenderedDigest == "" {
		t.Fatalf("expected the Agent's own marker digests to be read: %+v", target.Agent)
	}
	if target.Provider.PortableDigest != target.Agent.PortableDigest {
		t.Fatalf("expected matching bundles to agree: provider=%q agent=%q", target.Provider.PortableDigest, target.Agent.PortableDigest)
	}

	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded bundleStatusReport
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Targets[0].Provider.PortableDigest == "" || decoded.Targets[0].Agent.PortableDigest == "" {
		t.Fatalf("expected both marker digests to round-trip through JSON: %+v", decoded.Targets[0])
	}
}

func TestBundleDigestBehindFindsMatchWithinFileTouchingHistory(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	original := []byte("instructions: original\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, dir, "init", "--quiet")
	runBundleStatusGit(t, git, dir, "add", "agent.yaml")
	runBundleStatusGit(t, git, dir, "commit", "--quiet", "-m", "first")
	originalDigest := agentruntime.PortableBundleDigest(original)

	current := []byte("instructions: revised\n")
	if err := os.WriteFile(path, current, 0600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, dir, "add", "agent.yaml")
	runBundleStatusGit(t, git, dir, "commit", "--quiet", "-m", "second")

	ctx := t.Context()
	if sha, found := bundleFindDeployedCommit(ctx, dir, originalDigest); !found || sha == "" {
		t.Fatalf("expected the original commit to be found, got sha=%q found=%v", sha, found)
	}
	missing := agentruntime.PortableBundleDigest([]byte("never committed"))
	if _, found := bundleFindDeployedCommit(ctx, dir, missing); found {
		t.Fatal("expected no match for a digest outside history")
	}
}

// A bundle reached through a symlink is the same bundle. Git reports the
// repository root with symlinks resolved, so an unresolved bundle path looks
// like a path outside the repository. The symlink is made explicitly, so this
// fails on every platform rather than only where the temporary directory is
// itself behind one, as on macOS.
func TestBundleDigestBehindFindsMatchThroughASymlinkedPath(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	real := t.TempDir()
	content := []byte("instructions: deployed\n")
	if err := os.WriteFile(filepath.Join(real, "agent.yaml"), content, 0600); err != nil {
		t.Fatal(err)
	}
	runBundleStatusGit(t, git, real, "init", "--quiet")
	runBundleStatusGit(t, git, real, "add", "agent.yaml")
	runBundleStatusGit(t, git, real, "commit", "--quiet", "-m", "deployed")
	link := filepath.Join(t.TempDir(), "bundle-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sha, found := bundleFindDeployedCommit(t.Context(), link, agentruntime.PortableBundleDigest(content))
	if !found || sha == "" {
		t.Fatalf("the deployed commit was not found through a symlinked bundle path: sha=%q found=%v", sha, found)
	}
}
