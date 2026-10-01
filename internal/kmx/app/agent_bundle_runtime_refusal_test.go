package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

const (
	kagentOrkaOnlyError = "runtime kagent bundle is not supported by this Orka-only command"
	kagentBundleSource  = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: sample
spec:
  instructions: |
    Answer briefly.
  description: Sample Kagent agent
  model:
    name: gpt-4o-mini
extensions:
  kagent:
    apiVersion: kagent.dev/v1alpha2
    runtime: go
`
)

func replaceBundleAgentWithKagent(t *testing.T, bundle string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bundle, "agent.yaml"), []byte(kagentBundleSource), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireKagentOrkaOnlyError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), kagentOrkaOnlyError) {
		t.Fatalf("Kagent bundle error = %v, want %q", err, kagentOrkaOnlyError)
	}
}

func requireNoNewBundleKubectlCalls(t *testing.T, dir string, before int) {
	t.Helper()
	if calls := orkaCalls(t, dir); len(calls) != before {
		t.Fatalf("Kagent bundle reached kubectl: %+v", calls[before:])
	}
}

func TestLiftBundleRefusesKagentBeforeClusterReads(t *testing.T) {
	a, opt, dir, _ := liftBundleFixture(t)
	replaceBundleAgentWithKagent(t, opt.BundleDir)
	before := len(orkaCalls(t, dir))

	requireKagentOrkaOnlyError(t, a.LiftAgentBundle(opt))
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestBundleStatusRefusesKagentBeforeClusterReads(t *testing.T) {
	a, opt, dir, _, _ := bundleStatusFixture(t)
	replaceBundleAgentWithKagent(t, opt.BundleDir)
	opt.Context = "kind-test"
	before := len(orkaCalls(t, dir))

	requireKagentOrkaOnlyError(t, a.BundleStatus(opt))
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestEvaluateBundleRefusesKagentBeforeClusterReads(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"case.yaml": evalHelloCase})
	replaceBundleAgentWithKagent(t, f.bundle)
	before := len(orkaCalls(t, f.dir))

	requireKagentOrkaOnlyError(t, f.app.EvaluateAgentBundle(f.opt))
	requireNoNewBundleKubectlCalls(t, f.dir, before)
}

func TestConsoleBundleComparisonRefusesKagentBeforeClusterReads(t *testing.T) {
	a, root, bundle, dir, name, _, _ := consoleBundleFixture(t)
	replaceBundleAgentWithKagent(t, bundle)
	before := len(orkaCalls(t, dir))

	snapshot := a.consoleBundleStatus(t.Context(), consoleBundleEnv(), consoleBundleRow(name), root)
	if snapshot.Target != nil {
		t.Fatalf("Kagent bundle produced a comparison target: %+v", snapshot.Target)
	}
	if !strings.Contains(snapshot.Err, kagentOrkaOnlyError) {
		t.Fatalf("Kagent bundle error = %q, want %q", snapshot.Err, kagentOrkaOnlyError)
	}
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestConsoleBundleLiftRefusesKagentBeforeTargetWork(t *testing.T) {
	a, root, bundle, dir, name, _, _ := consoleBundleFixture(t)
	replaceBundleAgentWithKagent(t, bundle)
	before := len(orkaCalls(t, dir))
	action := agentTUIAction{
		kind: "lift", agent: consoleBundleRow(name), source: consoleBundleEnv(), bundles: root,
		create: &lift.Options{Cluster: "not-created", ResourceGroup: "not-created"},
	}

	_, err := a.runAgentTUIAction(action)
	requireKagentOrkaOnlyError(t, err)
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestInteractiveLiftRefusesKagentBeforeTargetDiscovery(t *testing.T) {
	b, renderer, opt, dir, _ := interactiveBundleFixture(t, "")
	replaceBundleAgentWithKagent(t, opt.BundleDir)
	before := len(orkaCalls(t, dir))

	requireKagentOrkaOnlyError(t, b.liftAgent(t.Context(), renderer))
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestInteractiveBundledLiftRefusesKagentBeforeClusterReads(t *testing.T) {
	b, renderer, opt, dir, _ := interactiveBundleFixture(t, "")
	replaceBundleAgentWithKagent(t, opt.BundleDir)
	before := len(orkaCalls(t, dir))

	requireKagentOrkaOnlyError(t, b.liftBundledAgentTo(t.Context(), renderer, chatLiftTarget{Context: "kind-test"}, opt.BundleDir))
	requireNoNewBundleKubectlCalls(t, dir, before)
}

func TestBundleRevisionCheckRefusesKagent(t *testing.T) {
	_, opt, dir, _ := liftBundleFixture(t)
	replaceBundleAgentWithKagent(t, opt.BundleDir)
	before := len(orkaCalls(t, dir))

	requireKagentOrkaOnlyError(t, checkBundlePlanRevision(opt.BundleDir, strings.Repeat("a", 64)))
	requireNoNewBundleKubectlCalls(t, dir, before)
}
