package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// liftFixtureOutput signals when the next input owner has actually rendered.
// Sending all keys up front lets the previous Bubble Tea program consume them;
// sleeping instead makes every plan/confirmation test pay an arbitrary delay.
type liftFixtureOutput struct {
	mu       sync.Mutex
	out      *bytes.Buffer
	nextPane chan struct{}
	panes    int
}

func (w *liftFixtureOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(p)
	// Bubble Tea hides the cursor when its next program takes input ownership,
	// including on non-terminal output where the view itself is not rendered.
	if strings.Contains(string(p), "\x1b[?25l") {
		w.panes++
		if w.panes == 2 {
			w.nextPane <- struct{}{}
		}
	}
	return n, err
}

func interactiveBundleFixture(t *testing.T, keys string) (*orkaChatBackend, *chatRenderer, LiftAgentBundleOptions, string, *bytes.Buffer) {
	t.Helper()
	a, opt, dir, _ := liftBundleFixture(t)
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestBundleLiftKubectlHelper"))
	t.Setenv("KMX_BUNDLE_INTERACTIVE_TEST", "1")
	root := filepath.Join(dir, "agents")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(opt.BundleDir, filepath.Join(root, "sample")); err != nil {
		t.Fatal(err)
	}
	opt.BundleDir = filepath.Join(root, "sample")
	var out bytes.Buffer
	output := &liftFixtureOutput{out: &out, nextPane: make(chan struct{}, 1)}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	first, rest, _ := strings.Cut(keys, "\r")
	// This wait includes subprocess preparation, not just picker rendering. Use
	// the existing test budget, reserving time to report failure and close pipes.
	// A direct test-binary run with no package deadline still needs a finite cap.
	deadline, bounded := t.Deadline()
	if bounded {
		deadline = deadline.Add(-time.Second)
	} else {
		deadline = time.Now().Add(time.Minute)
	}
	paneCtx, cancelPane := context.WithDeadline(t.Context(), deadline)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer writer.Close()
		_, _ = writer.WriteString(first + "\r")
		if rest != "" {
			select {
			case <-output.nextPane:
			case <-paneCtx.Done():
				if t.Context().Err() != nil {
					return
				}
				t.Errorf("lift fixture: next pane did not take input ownership: %v", paneCtx.Err())
				_, _ = writer.WriteString("\x03")
				return
			}
			if strings.HasSuffix(rest, "\r\r") {
				_, _ = writer.WriteString(strings.TrimSuffix(rest, "\r"))
				for start := time.Now(); time.Since(start) < 15*time.Second; {
					select {
					case <-t.Context().Done():
						return
					case <-time.After(time.Millisecond):
					}
					if entries, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts")); err == nil && len(entries) > 0 {
						// Let the completion event reach the deployment pane.
						select {
						case <-t.Context().Done():
							return
						case <-time.After(500 * time.Millisecond):
						}
						_, _ = writer.WriteString("\r")
						return
					}
				}
				if t.Context().Err() != nil {
					return
				}
				t.Error("lift fixture: deployment did not write its receipt")
				_, _ = writer.WriteString("\x03")
			} else {
				_, _ = writer.WriteString(rest)
			}
		}
	}()
	t.Cleanup(func() {
		cancelPane()
		_ = input.Close()
		_ = writer.Close()
		<-inputDone
	})
	a.Stdin = input
	a.Cfg.KubeContext = "kind-source"
	a.Out = output
	a.Err = output
	b := &orkaChatBackend{app: a, agent: "sample", namespace: OrkaNamespace, bundleRoot: root}
	return b, newChatRenderer(output), opt, dir, &out
}

// Removing the bundle route must make this test attempt to copy a live Agent
// instead of stopping safely at the read-only plan review.
func TestInteractiveBundleLiftGatePrecedesPreparation(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "inference\r")
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	t.Setenv("KMX_LIFT_MISSING", "controller")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "no evaluation receipt") {
		t.Fatalf("console offered preparation before gate refusal: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestInteractiveBundleLiftRefusesFailedEvaluationGate(t *testing.T) {
	b, r, opt, dir, out := interactiveBundleFixture(t, "inference\r")
	gatePolicy(t, opt.BundleDir, "cluster-uid", "staging-uid")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "no evaluation receipt") {
		t.Fatalf("console omitted gate condition: %v; %s", err, out.String())
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestInteractiveBundleLiftPlanCancelIsReadOnly(t *testing.T) {
	b, r, opt, dir, out := interactiveBundleFixture(t, "inference\r\r")
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
		t.Fatal(err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
	for _, want := range []string{"Lift destination:", "Provider/sample: created", "Agent/sample: created", "Plan only:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("review missing %q: %s", want, out.String())
		}
	}
}

// A valid read-only plan may take longer than a small picker-only watchdog,
// especially when the executable boundary is race-instrumented.
func TestInteractiveBundleLiftPlanCancelAllowsSlowPreparation(t *testing.T) {
	b, r, opt, dir, out := interactiveBundleFixture(t, "inference\r\r")
	t.Setenv("KMX_LIFT_PROVIDER_READ_DELAY", "4s")
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "provider-read-delayed")); err != nil {
		t.Fatalf("slow preparation was not exercised: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
	for _, want := range []string{"Lift destination:", "Provider/sample: created", "Agent/sample: created", "Plan only:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("review missing %q: %s", want, out.String())
		}
	}
}

func TestConsoleBundleLiftUsesDefaultTargetKubeconfigNotSourceSnapshot(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "inference\r\r")
	t.Setenv("KUBECONFIG", "")
	b.app.Run.Env = append(b.app.Run.Env, "KUBECONFIG=/tmp/source-snapshot-only")
	log := filepath.Join(dir, "target-kubeconfig")
	t.Setenv("KMX_BUNDLE_ENV_LOG", log)
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test", DefaultKubeconfig: true}); err != nil {
		t.Fatal(err)
	}
	used, err := os.ReadFile(log)
	if err != nil || string(used) == "/tmp/source-snapshot-only" {
		t.Fatalf("target reads used source-only kubeconfig: %q %v", used, err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundleLiftCustomNamespaceNeedsResultAccessBeforeDeploy(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	b.namespace = "custom"
	t.Setenv("KMX_BUNDLE_NO_RESULT_READER", "1")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "result ServiceAccount") || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("missing result access was accepted: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundleLiftRefusesMissingResultGrant(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	b.namespace = "custom"
	t.Setenv("KMX_BUNDLE_NO_RESULT_ROLE", "1")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "Task-get Role/RoleBinding") {
		t.Fatalf("missing result grant was accepted: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundleResultReaderRefusesWrongGrantOrSubject(t *testing.T) {
	for _, flag := range []string{"KMX_BUNDLE_BAD_RESULT_ROLE", "KMX_BUNDLE_LIMITED_RESULT_ROLE", "KMX_BUNDLE_BAD_RESULT_BINDING", "KMX_BUNDLE_BAD_RESULT_REF"} {
		t.Run(flag, func(t *testing.T) {
			b, _, _, _, _ := interactiveBundleFixture(t, "")
			t.Setenv(flag, "1")
			ready, err := bundleResultReaderPresent(t.Context(), b.app, OrkaNamespace)
			if err != nil {
				t.Fatal(err)
			}
			if ready {
				t.Fatalf("%s was accepted as Task-get access", flag)
			}
		})
	}
}

func TestInteractiveBundleLiftExcludesOwnAndStaleProviders(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	t.Setenv("KMX_BUNDLE_NO_READY", "1")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "Ready Provider") || !strings.Contains(err.Error(), "provision") {
		t.Fatalf("no eligible inference: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestInteractiveBundleLiftDeployWritesReceiptAndConnects(t *testing.T) {
	b, r, opt, dir, out := interactiveBundleFixture(t, "inference\rj\r\r")
	t.Setenv("KMX_RECONCILE_REMOTE", "1")
	t.Setenv("KMX_BUNDLE_NO_APPLY", "1")
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt: %v %v", receipts, err)
	}
	data, err := os.ReadFile(filepath.Join(opt.BundleDir, "receipts", receipts[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleLiftReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Receipt.Target.Context != "kind-test" || receipt.Receipt.Resources[1].Outcome != runtime.ResourceCreated {
		t.Fatalf("shared deploy not used: %+v", receipt)
	}
	for _, want := range []string{"Connected to lifted Agent", "kmx agent lift", "--inference provider:inference", "send"} {
		if !strings.Contains(strings.ToLower(out.String()), strings.ToLower(want)) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if b.app.Cfg.KubeContext != "kind-test" {
		t.Fatalf("not connected: %+v", b.app.Cfg)
	}
	marked := map[string]bool{}
	for _, call := range orkaCalls(t, dir) {
		if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
			t.Fatalf("unpinned kubectl: %v", call.Args)
		}
		if call.Document == nil || slices.Contains(call.Args, "--dry-run=server") {
			continue
		}
		kind, _ := call.Document["kind"].(string)
		if kind != "Provider" && kind != "Agent" {
			continue
		}
		metadata, _ := call.Document["metadata"].(map[string]any)
		annotations, _ := metadata["annotations"].(map[string]any)
		if annotations["kaimahi.dev/bundle"] == nil || annotations["kaimahi.dev/portable-digest"] == nil || annotations["kaimahi.dev/rendered-digest"] == nil {
			t.Fatalf("%s missing bundle ownership markers: %+v", kind, metadata)
		}
		marked[kind] = true
	}
	if !marked["Provider"] || !marked["Agent"] {
		t.Fatalf("bundle deployment did not stamp both resource kinds: %v", marked)
	}
}

func TestInteractiveBundleLiftMissingBundleLabelsUntrackedLegacyRoute(t *testing.T) {
	b, r, opt, _, out := interactiveBundleFixture(t, "")
	if err := os.Rename(opt.BundleDir, filepath.Join(filepath.Dir(opt.BundleDir), "elsewhere")); err != nil {
		t.Fatal(err)
	}
	_ = b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if !strings.Contains(out.String(), "legacy live-copy") || !strings.Contains(out.String(), "untracked by kmx agent status/evaluate") {
		t.Fatalf("missing explicit fallback warning: %s", out.String())
	}
}

// No local bundle means the console copies a live Agent. It must inspect the
// target Tool and refuse before showing the final deploy action or writing it.
func TestLiveCopyLiftOffersRepairOnlyForQuickstartPolicyDenial(t *testing.T) {
	for _, tc := range []struct {
		name, response, policy      string
		apiFailure, repair, repoint bool
	}{
		{"denied", `{"status":{"allowed":false,"denied":true}}`, quickstartK8sToolPolicy, false, true, false},
		{"no opinion", `{"status":{"allowed":false,"denied":false}}`, quickstartK8sToolPolicy, false, true, false},
		{"repointed before repair", `{"status":{"allowed":false,"denied":false}}`, quickstartK8sToolPolicy, false, false, true},
		{"api failure", "", quickstartK8sToolPolicy, true, false, false},
		{"evaluation error", `{"status":{"allowed":false,"denied":false,"evaluationError":"cannot decide"}}`, quickstartK8sToolPolicy, false, false, false},
		{"conflicting", `{"status":{"allowed":true,"denied":true}}`, quickstartK8sToolPolicy, false, false, false},
		{"malformed", `{"status":"invalid"}`, quickstartK8sToolPolicy, false, false, false},
		{"custom policy", `{"status":{"allowed":false,"denied":false}}`, "another-policy", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r, opt, dir, _ := interactiveBundleFixture(t, "inference\r\r")
			if err := os.RemoveAll(opt.BundleDir); err != nil {
				t.Fatal(err)
			}
			source, err := createOrkaBundle(CreateOptions{Name: "sample", Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", BaseURL: "https://source.example.invalid/v1", Secret: "source-secret", Tools: quickstartK8sTool})
			if err != nil {
				t.Fatal(err)
			}
			agentJSON, err := json.Marshal(source.Agent)
			if err != nil {
				t.Fatal(err)
			}
			providerJSON, err := json.Marshal(source.Provider)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_CHAT_LIVE_POLICY_TEST", "1")
			t.Setenv("KMX_CHAT_SOURCE_AGENT", string(agentJSON))
			t.Setenv("KMX_CHAT_SOURCE_PROVIDER", string(providerJSON))
			t.Setenv("KMX_LIFT_POLICY_TEST", "1")
			t.Setenv("KMX_LIFT_CONTROLLER_NAME", "orka-controller")
			t.Setenv("KMX_LIFT_TOOL_SPEC", fmt.Sprintf(`{"http":{"outboundAccessPolicyRef":{"name":%q}}}`, tc.policy))
			t.Setenv("KMX_LIFT_POLICY_REVIEW", tc.response)
			if tc.apiFailure {
				t.Setenv("KMX_LIFT_POLICY_API_FAIL", "1")
			}
			if tc.repoint {
				t.Setenv("KMX_LIFT_REPOINT_AFTER_PREFLIGHT", "1")
			}
			err = b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
			if tc.repoint && (err == nil || !strings.Contains(err.Error(), "different cluster")) {
				t.Fatalf("repointed preparation was not refused: %v", err)
			}
			if tc.repair {
				if err != nil {
					t.Fatalf("cancelled repair failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("lift accepted an unrepairable policy review")
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
			reviewedTarget := false
			for _, call := range orkaCalls(t, dir) {
				if !slices.Contains(call.Args, "--raw") {
					continue
				}
				if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
					t.Fatalf("policy review used source or ambient cluster: %v", call.Args)
				}
				reviewedTarget = true
			}
			if !reviewedTarget {
				t.Fatal("live-copy lift refused without target authorization review")
			}
		})
	}
}

func TestInteractiveBundleLiftInvalidBundleDoesNotFallback(t *testing.T) {
	b, r, opt, dir, out := interactiveBundleFixture(t, "")
	if err := os.Remove(filepath.Join(opt.BundleDir, "bindings.yaml")); err != nil {
		t.Fatal(err)
	}
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "invalid") || strings.Contains(out.String(), "legacy live-copy") {
		t.Fatalf("invalid bundle accepted or fell back: %v; %s", err, out.String())
	}
	if len(orkaCalls(t, dir)) != 0 {
		t.Fatal("invalid bundle reached destination")
	}
}

func TestInteractiveBundleLiftMissingOrkaOffersSeparatePreparation(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	t.Setenv("KMX_LIFT_MISSING", "crd")
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
		t.Fatal(err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
	for _, call := range orkaCalls(t, dir) {
		if slices.Contains(call.Args, "providers.core.orka.ai") && !slices.Contains(call.Args, "crd") {
			t.Fatalf("missing Orka continued to Provider selection: %v", call.Args)
		}
	}
}

func TestInteractiveBundleLiftUnavailableControllerPreparesBeforeProviderList(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	t.Setenv("KMX_LIFT_MISSING", "controller")
	if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
		t.Fatalf("cancelled preparation should not fail due to Provider list: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestInteractiveBundleLiftDoesNotOfferInstallForUnreachableController(t *testing.T) {
	b, r, opt, dir, _ := interactiveBundleFixture(t, "\r")
	t.Setenv("KMX_BUNDLE_CONTROLLER_UNREACHABLE", "1")
	err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
	if err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("controller read failure should be reported: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundlePreparationRefusesRepointedContext(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	uid, err := a.liftClusterUID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_LIFT_CLUSTER_UID", "another-cluster")
	b := &orkaChatBackend{agent: "sample", namespace: OrkaNamespace}
	if err := b.guardBundlePreparation(t.Context(), a, uid); err == nil || !strings.Contains(err.Error(), "different cluster") {
		t.Fatalf("preparation reached a repointed context: %v", err)
	}
	if a.guarded {
		t.Fatal("repointed target marked as authorized")
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundlePreparationGuardUsesPinnedRemoteContext(t *testing.T) {
	a, _, _, _ := liftBundleFixture(t)
	t.Setenv("KMX_RECONCILE_REMOTE", "1")
	a.Stdin = nil
	b := &orkaChatBackend{agent: "sample", namespace: OrkaNamespace}
	uid, err := a.liftClusterUID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.guardBundlePreparation(t.Context(), a, uid); err != nil {
		t.Fatalf("confirmed preparation on remote target refused: %v", err)
	}
	if !a.guarded {
		t.Fatal("preparation did not pass the existing guard")
	}
}

func TestCancelledBundleDeployWarnsAboutPartialWrites(t *testing.T) {
	err := bundleDeployError(context.Canceled)
	if err == nil || !strings.Contains(err.Error(), "may have changed") || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("cancel hid partial deployment risk: %v", err)
	}
}

func TestBundleReviewRejectsRemovedResultAccess(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestBundleLiftKubectlHelper"))
	t.Setenv("KMX_BUNDLE_INTERACTIVE_TEST", "1")
	t.Setenv("KMX_BUNDLE_NO_RESULT_ROLE", "1")
	if err := confirmBundleResultAccess(t.Context(), a, OrkaNamespace); err == nil || !strings.Contains(err.Error(), "result access") {
		t.Fatalf("missing grant was accepted after review: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundleReviewRejectsRepointedContext(t *testing.T) {
	a, _, _, _ := liftBundleFixture(t)
	uid, err := a.liftClusterUID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_LIFT_CLUSTER_UID", "another-cluster")
	if err := confirmBundleTargetUID(t.Context(), a, uid); err == nil || !strings.Contains(err.Error(), "different cluster") {
		t.Fatalf("context repointing was accepted: %v", err)
	}
}

func TestBundlePlanMustStillMatchBeforeDeployment(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	opt.Plan = true
	if err := confirmBundlePlan(t.Context(), a, opt, "Provider/sample: reused"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale plan was accepted: %v", err)
	}
	assertNoLiftWrites(t, dir, opt.BundleDir)
}

func TestBundleRevisionChangeRequiresAnotherPlan(t *testing.T) {
	_, opt, _, _ := liftBundleFixture(t)
	_, _, digest, err := readBundlePortableAgent(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opt.BundleDir, "agent.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte("\n# changed during review\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkBundlePlanRevision(opt.BundleDir, digest); err == nil || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("changed bundle was deployed without a fresh plan: %v", err)
	}
}

func TestBundlePlanCannotBeConfirmedAfterOutputOverflow(t *testing.T) {
	notes := &bundlePlanNotes{buffer: orkaBoundedBuffer{remaining: 4}}
	_, _ = fmt.Fprint(notes, "long resource decision")
	if notes.complete() {
		t.Fatal("truncated plan was offered for confirmation")
	}
}

func TestBundlePlanFitMeasuresActualPickerTitle(t *testing.T) {
	title := strings.Repeat("x", 83) + strings.Repeat("\nitem", 11)
	if !chatPickerTitleFits(title, 88, 24, true) {
		t.Fatal("test title does not fit without picker prefix")
	}
	if bundlePlanTitleFits(title, 88, 24) {
		t.Fatal("prefix wrapped and hid the last plan decision")
	}
}

func TestBundlePlanCannotBeConfirmedWhenTitleWouldBeTruncated(t *testing.T) {
	title := "Review lift\nProvider/sample: updated\nAgent/sample: updated"
	if chatPickerTitleFits(title, 40, 8, true) {
		t.Fatal("short terminal would hide resource outcomes")
	}
	if !chatPickerTitleFits(title, 88, 30, true) {
		t.Fatal("ordinary terminal should display complete plan")
	}
	if chatPickerTitleFits(strings.Repeat("a long changed field path ", 120), 88, 30, true) {
		t.Fatal("wrapped plan decisions must not be silently cut")
	}
}

func bundleWithQuickstartReference(t *testing.T, path string) {
	t.Helper()
	file := filepath.Join(path, "agent.yaml")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(body, []byte("name: search"), []byte("name: "+quickstartK8sTool), 1)
	if bytes.Equal(body, updated) {
		t.Fatal("fixture has no search Tool reference")
	}
	if err := os.WriteFile(file, updated, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveBundleLiftOffersRepairForMissingQuickstartGrant(t *testing.T) {
	for _, tc := range []struct{ name, review string }{
		{"explicit denial", `{"status":{"allowed":false,"denied":true}}`},
		{"no opinion", `{"status":{"allowed":false,"denied":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r, opt, dir, _ := interactiveBundleFixture(t, "inference\r\r")
			bundleWithQuickstartReference(t, opt.BundleDir)
			t.Setenv("KMX_LIFT_POLICY_TEST", "1")
			t.Setenv("KMX_LIFT_CONTROLLER_NAME", "orka-controller")
			t.Setenv("KMX_LIFT_TOOL_SPEC", `{"http":{"outboundAccessPolicyRef":{"name":"kmx-k8s-tool-gateway"}}}`)
			t.Setenv("KMX_LIFT_POLICY_REVIEW", tc.review)
			if err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"}); err != nil {
				t.Fatalf("cancelled repair failed: %v", err)
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
		})
	}
}

func TestInteractiveBundleLiftDoesNotRepairUncertainOrCustomPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, policy, review, want string
		apiFailure                 bool
	}{
		{"API failure", quickstartK8sToolPolicy, "", "cannot evaluate", true},
		{"evaluation error", quickstartK8sToolPolicy, `{"status":{"allowed":false,"denied":false,"evaluationError":"cannot decide"}}`, "indeterminate", false},
		{"conflicting", quickstartK8sToolPolicy, `{"status":{"allowed":true,"denied":true}}`, "indeterminate", false},
		{"malformed", quickstartK8sToolPolicy, `{"status":"invalid"}`, "invalid", false},
		{"custom policy no opinion", "other-policy", `{"status":{"allowed":false,"denied":false}}`, "other-policy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r, opt, dir, _ := interactiveBundleFixture(t, "inference\r\r")
			bundleWithQuickstartReference(t, opt.BundleDir)
			t.Setenv("KMX_LIFT_POLICY_TEST", "1")
			t.Setenv("KMX_LIFT_CONTROLLER_NAME", "orka-controller")
			t.Setenv("KMX_LIFT_TOOL_SPEC", fmt.Sprintf(`{"http":{"outboundAccessPolicyRef":{"name":%q}}}`, tc.policy))
			t.Setenv("KMX_LIFT_POLICY_REVIEW", tc.review)
			if tc.apiFailure {
				t.Setenv("KMX_LIFT_POLICY_API_FAIL", "1")
			}
			err := b.liftAgentTo(t.Context(), r, chatLiftTarget{Context: "kind-test"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unrepairable policy offered repair or lost diagnostic: %v", err)
			}
			assertNoLiftWrites(t, dir, opt.BundleDir)
		})
	}
}

func TestBundlePreparationRefusesUnknownToolState(t *testing.T) {
	b := &orkaChatBackend{}
	if b.bundleLiftPreparable(OrkaNamespace, fmt.Errorf("cannot inspect Tool/k8s-get-resources in namespace orka-system: kubectl request failed: access forbidden")) {
		t.Fatal("a failed Tool read was treated as a missing Tool")
	}
	if !b.bundleLiftPreparable(OrkaNamespace, fmt.Errorf("Tool/k8s-get-resources in namespace orka-system is not Available; repair it")) {
		t.Fatal("a known unavailable Tool cannot be prepared")
	}
	if b.bundleLiftPreparable("custom", fmt.Errorf("Tool/k8s-get-resources in namespace custom is not Available")) {
		t.Fatal("fixed-namespace installer was offered in custom namespace")
	}
	for _, tc := range []struct {
		namespace, tool, policy string
		want                    bool
	}{
		{OrkaNamespace, quickstartK8sTool, quickstartK8sToolPolicy, true},
		{OrkaNamespace, "custom-tool", quickstartK8sToolPolicy, false},
		{OrkaNamespace, quickstartK8sTool, "custom-policy", false},
		{"custom", quickstartK8sTool, quickstartK8sToolPolicy, false},
	} {
		err := fmt.Errorf("preflight failed: %w", &policyPermissionDenied{Tool: tc.tool, Worker: "orka-ai-worker", Namespace: tc.namespace, Policy: tc.policy})
		if b.bundleLiftPreparable(tc.namespace, err) != tc.want {
			t.Fatalf("wrong repair classification for %s/%s %s (want %t)", tc.namespace, tc.tool, tc.policy, tc.want)
		}
	}
}

func TestBundleLiftCommandQuotesAllSelectedArguments(t *testing.T) {
	got := bundleLiftCommand("agents/agent's name", "remote context", "other-space", "inference")
	want := `kmx agent lift 'agents/agent'"'"'s name' --to-context 'remote context' --to-namespace other-space --inference provider:inference`
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
