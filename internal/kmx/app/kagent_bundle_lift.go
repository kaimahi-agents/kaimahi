package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type kagentLiftOperation struct {
	Plan       bool
	ClusterUID string
	Expected   *agentruntime.DeployReceipt
	Recheck    func() error
	bundleInfo os.FileInfo
}

// Open the parent once and use its descriptor for the final no-follow open.
// A recheck also compares parent and file identities, so identical bytes in a
// replacement directory/file cannot silently rebind reviewed local inputs.
func readKagentDesiredFile(path string, parentInfo, fileInfo os.FileInfo) ([]byte, os.FileInfo, os.FileInfo, error) {
	dir, err := os.OpenFile(filepath.Dir(path), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open Kagent desired input directory: %w", err)
	}
	defer dir.Close()
	parent, err := dir.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	if parentInfo != nil && !os.SameFile(parent, parentInfo) {
		return nil, nil, nil, fmt.Errorf("Kagent desired input directory changed")
	}
	file, err := openKagentFile(dir, filepath.Base(path))
	if err != nil {
		return nil, nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, nil, err
	}
	if fileInfo != nil && !os.SameFile(info, fileInfo) {
		return nil, nil, nil, fmt.Errorf("Kagent desired input file changed")
	}
	raw, err := readKagentOpenedFile(file, info, false)
	return raw, parent, info, err
}

func validateBundleRuntime(runtime, version, bindings string) error {
	switch runtime {
	case "", "orka":
		if version != "" || bindings != "" {
			return fmt.Errorf("--runtime-version and --bindings require explicit --runtime kagent")
		}
	case "kagent":
		if version != "v0.10.3" {
			return fmt.Errorf("Kagent bundle lift/status requires explicit --runtime-version v0.10.3")
		}
	default:
		return fmt.Errorf("unsupported bundle runtime")
	}
	return nil
}

// Both lifecycle verbs read author-owned desired inputs, never live bindings.
func (a *App) prepareKagentBundle(bundlePath, bindingPath, contextName, namespace string) (*App, string, kagentRuntimeAdapter, agentruntime.RenderedBundle, func() error, error) {
	fail := func(err error) (*App, string, kagentRuntimeAdapter, agentruntime.RenderedBundle, func() error, error) {
		return nil, "", kagentRuntimeAdapter{}, agentruntime.RenderedBundle{}, nil, err
	}
	if contextName == "" {
		return fail(fmt.Errorf("Kagent bundle lift/status requires explicit --to-context"))
	}
	if a.Cfg == nil || a.Run == nil {
		return fail(fmt.Errorf("Kagent lift/status requires configured kubectl and streams"))
	}
	for _, value := range []string{contextName, bundlePath, bindingPath, namespace} {
		if err := scaffold.RefuseKeyShapes(value); err != nil {
			return fail(fmt.Errorf("refusing credential-shaped Kagent target input"))
		}
	}
	bundle, err := resolveOrkaPath(bundlePath)
	if err != nil {
		return fail(err)
	}
	info, err := os.Lstat(bundle)
	if err != nil || !info.IsDir() {
		return fail(fmt.Errorf("Kagent bundle must be a directory"))
	}
	sourcePath := filepath.Join(bundle, "agent.yaml")
	if bindingPath == "" {
		bindingPath = filepath.Join(bundle, "bindings.yaml")
	}
	bindingPath, err = filepath.Abs(bindingPath)
	if err != nil {
		return fail(err)
	}
	source, sourceParent, sourceInfo, err := readKagentDesiredFile(sourcePath, info, nil)
	if err != nil {
		return fail(err)
	}
	var expectedBindingParent os.FileInfo
	if filepath.Dir(bindingPath) == bundle {
		expectedBindingParent = sourceParent
	}
	rawBindings, bindingParent, bindingInfo, err := readKagentDesiredFile(bindingPath, expectedBindingParent, nil)
	if err != nil {
		return fail(err)
	}
	bindings, err := agentruntime.ParseKagentBindings(rawBindings)
	if err != nil {
		return fail(fmt.Errorf("Kagent bindings: %w", err))
	}
	if namespace != "" && namespace != bindings.Namespace {
		return fail(fmt.Errorf("--to-namespace conflicts with KagentBindings namespace"))
	}
	worker := *a
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = contextName, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	profile := kagentLiftProfile()
	if a.kagentProfile != nil && a.kagentProfile.Version == profile.Version {
		profile = *a.kagentProfile
	}
	worker.kagentProfile = &profile
	adapter := kagentRuntimeAdapter{app: &worker, bindings: &bindings, lift: &kagentLiftOperation{bundleInfo: sourceParent}}
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		return fail(err)
	}
	rendered, err := adapter.Render(a.operationContext(), prepared, agentruntime.RenderOptions{})
	if err != nil {
		return fail(err)
	}
	recheck := func() error {
		current, _, _, err := readKagentDesiredFile(sourcePath, sourceParent, sourceInfo)
		if err != nil || !bytes.Equal(current, source) {
			return fmt.Errorf("agent.yaml changed during Kagent operation; no later writes attempted")
		}
		current, _, _, err = readKagentDesiredFile(bindingPath, bindingParent, bindingInfo)
		if err != nil || !bytes.Equal(current, rawBindings) {
			return fmt.Errorf("Kagent bindings changed during operation; no later writes attempted")
		}
		return nil
	}
	return &worker, bundle, adapter, rendered, recheck, nil
}

func (a *App) liftKagentBundle(opt LiftAgentBundleOptions) error {
	policyPath := filepath.Join(opt.BundleDir, "lift-policy.yaml")
	if _, err := os.Lstat(policyPath); !os.IsNotExist(err) {
		return fmt.Errorf("Kagent lift refuses present or unreadable lift-policy.yaml: evaluation promotion gates are not supported")
	}
	if opt.Inference != "" || opt.RequireEvaluated != "" || opt.OverrideGate != "" {
		return fmt.Errorf("Kagent lift refuses Orka inference and evaluation gate flags")
	}
	worker, bundle, adapter, rendered, recheck, err := a.prepareKagentBundle(opt.BundleDir, opt.Bindings, opt.ToContext, opt.ToNamespace)
	if err != nil {
		return err
	}
	targetBundle, err := kagentBundleFromRendered(rendered)
	if err != nil {
		return err
	}
	runtimeName := targetBundle.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["runtime"].(string)
	if _, _, _, err := worker.selectedKagentProfile().runtimeImage(runtimeName); err != nil {
		return err
	}
	root, err := os.OpenFile(bundle, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer root.Close()
	openedBundle, err := root.Stat()
	if err != nil || !os.SameFile(openedBundle, adapter.lift.bundleInfo) {
		return fmt.Errorf("Kagent bundle directory changed while opening receipt history")
	}
	if err = checkKagentReceiptDirs(root, false); err != nil {
		return err
	}
	if !opt.Plan {
		unlock, err := lockKagentLift(root)
		if err != nil {
			return err
		}
		defer unlock()
	}
	ctx := worker.operationContext()
	// Consent precedes installation reads and even dry-run admission calls.
	guardOpt := CreateOptions{Name: kagentObjectName(targetBundle.Agent), Namespace: kagentObjectNamespace(targetBundle.Agent), DryRun: opt.Plan}
	args := []string{"agent", "lift", opt.BundleDir, "--runtime", "kagent", "--runtime-version", "v0.10.3", "--to-context", opt.ToContext, "--to-namespace", guardOpt.Namespace}
	if opt.Bindings != "" {
		args = append(args, "--bindings", opt.Bindings)
	}
	action := "lift Kagent bundle: create ModelConfig and Agent in " + guardOpt.Namespace
	if opt.Plan {
		args = append(args, "--plan")
		action = "plan Kagent bundle: server dry-run resources in " + guardOpt.Namespace
	}
	if err = worker.guardKagentOperation(ctx, guardOpt.Namespace, action, worker.operationCommand(args...)); err != nil {
		return err
	}
	clusterUID, err := worker.kagentClusterUID(ctx)
	if err != nil {
		return err
	}
	target := agentruntime.DeployTarget{Runtime: agentruntime.Kagent, Context: opt.ToContext, Namespace: guardOpt.Namespace}
	name := kagentLiftReceiptName(target.Context, target.Namespace, clusterUID)
	expected, err := readKagentLiftReceiptAt(root, name, target, clusterUID, guardOpt.Name)
	if err != nil {
		return fmt.Errorf("Kagent lift receipt conflict: %w", err)
	}
	inputRecheck := recheck
	recheck = func() error {
		if _, err := os.Lstat(policyPath); !os.IsNotExist(err) {
			return fmt.Errorf("Kagent lift refuses lift-policy.yaml added during operation")
		}
		return inputRecheck()
	}
	adapter.lift = &kagentLiftOperation{Plan: opt.Plan, ClusterUID: clusterUID, Expected: expected, Recheck: recheck}
	deployed, err := adapter.Deploy(ctx, rendered, agentruntime.DeployOptions{})
	if err != nil {
		return err
	}
	if opt.Plan {
		worker.notef("Plan only: no resources, artifact, receipt or remembered target written")
		return nil
	}
	for _, resource := range deployed.Receipt.Resources {
		worker.notef("%s/%s: %s", resource.Kind, resource.Name, resource.Outcome)
	}
	if expected != nil {
		worker.notef("Receipt-bound ready deployment reused read-only; no model response tested")
		return nil
	}
	if err = recheck(); err != nil {
		return fmt.Errorf("Agent deployed but unrecorded: %w", err)
	}
	wrapped := bundleLiftReceipt{Receipt: deployed.Receipt, GitCommit: liftAgentCommit(ctx, bundle, rendered.PortableDigest())}
	if err = writeKagentLiftReceipt(root, name, wrapped); err != nil {
		return fmt.Errorf("Agent deployed but receipt was not saved; inspect before retrying: %w", err)
	}
	worker.notef("Kagent deployment readiness recorded; no model response tested")
	return nil
}
