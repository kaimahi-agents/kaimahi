package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// LiftAgentBundleOptions selects a bundle and a destination independent of
// kubeconfig's current context. Omitted target and inference reuse only a
// previous successful lift of this exact local bundle path.
type LiftAgentBundleOptions struct {
	BundleDir, ToContext, ToNamespace, Inference string
	RequireEvaluated, OverrideGate               string
	Plan                                         bool
}

type bundleLiftSelection struct {
	Context    string `json:"context"`
	ClusterUID string `json:"clusterUID"`
	Namespace  string `json:"namespace"`
	Inference  string `json:"inference"`
}

type bundleLiftReceipt struct {
	Receipt             agentruntime.DeployReceipt `json:"receipt"`
	GitCommit           string                     `json:"gitCommit"`
	GateOverrideReason  string                     `json:"gateOverrideReason,omitempty"`
	GateFailedCondition string                     `json:"gateFailedCondition,omitempty"`
}

// LiftAgentBundle deploys only an Orka portable agent.yaml. In particular it
// does not copy a source Provider, credentials or Tasks from another cluster.
func (a *App) LiftAgentBundle(opt LiftAgentBundleOptions) error {
	ctx := a.operationContext()
	if a.Cfg == nil || a.Run == nil {
		return fmt.Errorf("lift requires configured kubectl and streams")
	}
	bundle, err := resolveOrkaPath(opt.BundleDir)
	if err != nil {
		return fmt.Errorf("resolve bundle: %w", err)
	}
	if err := scaffold.RefuseKeyShapes(bundle); err != nil {
		return fmt.Errorf("refusing credential-shaped bundle path")
	}
	if err := checkLiftBundle(bundle); err != nil {
		return err
	}
	if _, _, _, err := readBundlePortableAgent(bundle); err != nil {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	policy, err := loadBundleLiftPolicy(bundle)
	if err != nil {
		return err
	}
	selectionPath, err := bundleLiftSelectionPath(bundle)
	if err != nil {
		return err
	}
	remembered, err := loadBundleLiftSelection(selectionPath)
	if err != nil {
		return err
	}
	reusedContext := opt.ToContext == ""
	contextName := opt.ToContext
	if reusedContext {
		contextName = remembered.Context
	}
	if contextName == "" {
		return fmt.Errorf("lift requires --to-context (no target remembered for this bundle)")
	}
	inference := opt.Inference
	if inference == "" {
		if remembered.Context == "" || remembered.Context != contextName {
			return fmt.Errorf("lift requires --inference provider:<name> for this target")
		}
		inference = remembered.Inference
	}
	provider, ok := strings.CutPrefix(inference, "provider:")
	if !ok || scaffold.ValidateObjectName(provider) != nil {
		return fmt.Errorf("--inference must name an existing provider:<name>")
	}
	namespace := opt.ToNamespace
	if namespace == "" {
		namespace = OrkaNamespace
		if remembered.Context == contextName && remembered.Namespace != "" {
			namespace = remembered.Namespace
		}
	}
	if scaffold.ValidateNamespace(namespace) != nil {
		return fmt.Errorf("invalid destination namespace (expected a Kubernetes RFC 1123 label)")
	}
	if reusedContext && (remembered.Context == "" || remembered.ClusterUID == "" || remembered.Inference == "") {
		return fmt.Errorf("ambiguous remembered target; pass --to-context and --inference explicitly")
	}
	if opt.Inference == "" && remembered.Namespace != namespace {
		return fmt.Errorf("remembered inference is for namespace %s; pass --inference explicitly", remembered.Namespace)
	}
	worker := *a
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = contextName, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	// Every kubectl invocation, including kubeconfig inspection, is pinned to
	// the chosen destination. A separate App avoids changing ambient routing.
	raw, err := worker.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read destination context metadata: %w", err)
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode destination context metadata")
	}
	cluster, err := liftContextCluster(kube, contextName)
	if err != nil {
		return err
	}
	uid, err := worker.liftClusterUID(ctx)
	if err != nil {
		return err
	}
	if remembered.Context == contextName && remembered.ClusterUID != "" && remembered.ClusterUID != uid {
		return fmt.Errorf("stale remembered target: context %s now identifies another cluster; remove local selection %s before retrying with --to-context and --inference", contextName, selectionPath)
	}
	if opt.OverrideGate != "" && strings.TrimSpace(opt.OverrideGate) == "" {
		return fmt.Errorf("--override-gate requires a non-empty reason")
	}
	name, _, digest, err := readBundlePortableAgent(bundle)
	if err != nil {
		return err
	}
	destination := bundleGateTarget{ClusterUID: uid, Namespace: namespace}
	required, failed := evaluateBundleLiftGate(bundle, name, digest, destination, opt.RequireEvaluated)
	if opt.OverrideGate != "" && !required {
		return fmt.Errorf("--override-gate requires an evaluation gate")
	}
	if required {
		switch {
		case failed == "":
			worker.notef("Gate: pass")
		case opt.OverrideGate != "" && !opt.Plan:
			worker.notef("Gate: overridden (%s); reason: %s", failed, opt.OverrideGate)
		default:
			worker.notef("Gate: refused (%s)", failed)
			return fmt.Errorf("lift evaluation gate refused: %s", failed)
		}
	} else {
		worker.notef("Gate: not required")
	}
	worker.notef("Lift destination: context %s, cluster %s, namespace %s", contextName, cluster, namespace)
	if err := worker.liftPrerequisites(ctx, namespace); err != nil {
		return err
	}
	bindings, err := worker.liftProviderBindings(ctx, namespace, provider)
	if err != nil {
		return err
	}
	if err := worker.orkaProviderSecretPresent(ctx, namespace, bindings.Provider.SecretRef.Name, bindings.Provider.SecretRef.Key); err != nil {
		return err
	}
	rendered, err := RenderOrkaBundleFile(bundle, bindings)
	if err != nil {
		return err
	}
	renderedBundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		return err
	}
	if provider == orkaObjectName(renderedBundle.Provider) {
		return fmt.Errorf("selected Provider/%s is the Agent's target Provider; lift would modify the selected Provider", provider)
	}
	if rendered.PortableDigest() != digest {
		return fmt.Errorf("agent.yaml changed during gate check; retry lift")
	}
	if err := worker.liftToolsAvailable(ctx, namespace, renderedBundle); err != nil {
		return err
	}
	if err := worker.liftAllowedAgentsPresent(ctx, namespace, renderedBundle); err != nil {
		return err
	}
	adapter := orkaRuntimeAdapter{app: &worker, create: &CreateOptions{Namespace: namespace, Name: orkaObjectName(renderedBundle.Agent), Secret: bindings.Provider.SecretRef.Name}}
	if opt.Plan {
		checks, err := worker.planOrkaReconcile(ctx, rendered, renderedBundle, namespace)
		if err != nil {
			worker.notef("Plan: resource comparison unknown where prerequisites or server admission prevent a decision")
			return err
		}
		for _, check := range checks {
			worker.printLiftDecision(check)
		}
		worker.notef("Plan only: no resources, receipt or remembered target written")
		return nil
	}
	// Recheck local evidence immediately before the first resource mutation.
	if _, _, currentDigest, err := readBundlePortableAgent(bundle); err != nil || currentDigest != digest {
		return fmt.Errorf("agent.yaml changed during preflight; retry lift")
	}
	currentPolicy, err := loadBundleLiftPolicy(bundle)
	if err != nil {
		return err
	}
	if !slices.Equal(policy.Rules, currentPolicy.Rules) {
		return fmt.Errorf("lift policy changed during preflight; retry lift")
	}
	currentRequired, currentFailure := evaluateBundleLiftGate(bundle, name, digest, destination, opt.RequireEvaluated)
	if currentRequired != required || currentFailure != failed {
		return fmt.Errorf("lift evaluation gate changed during preflight: %s", currentFailure)
	}
	currentUID, err := worker.liftClusterUID(ctx)
	if err != nil {
		return fmt.Errorf("cannot verify destination cluster identity before deployment: %w", err)
	}
	if currentUID != uid {
		return fmt.Errorf("destination cluster identity changed during preflight; retry lift")
	}
	deployed, err := adapter.Deploy(ctx, rendered, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		return fmt.Errorf("lift deployment failed; Provider or Agent may have been changed before failure (inspect the destination before retrying): %w", err)
	}
	for _, resource := range deployed.Receipt.Resources {
		worker.notef("%s/%s: %s", resource.Kind, resource.Name, resource.Outcome)
	}
	gitCommit := liftAgentCommit(ctx, bundle, rendered.PortableDigest())
	if gitCommit == "uncommitted" {
		worker.notef("Warning: agent.yaml is uncommitted (%s)", liftAgentUncommittedReason(ctx, bundle))
	}
	wrapped := bundleLiftReceipt{Receipt: deployed.Receipt, GitCommit: gitCommit}
	if failed != "" && opt.OverrideGate != "" {
		wrapped.GateOverrideReason, wrapped.GateFailedCondition = opt.OverrideGate, failed
	}
	if err := writeLiftReceipt(bundle, uid, wrapped); err != nil {
		return fmt.Errorf("Agent deployed but receipt was not saved: %w", err)
	}
	if err := saveBundleLiftSelection(selectionPath, bundleLiftSelection{Context: contextName, ClusterUID: uid, Namespace: namespace, Inference: inference}); err != nil {
		return fmt.Errorf("Agent deployed and receipt saved but target selection was not remembered: %w", err)
	}
	return nil
}

func checkLiftBundle(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read bundle directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bundle must be a directory")
	}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		entry, err := os.Lstat(filepath.Join(path, name))
		if err != nil {
			return fmt.Errorf("bundle requires %s: %w", name, err)
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("bundle %s must be a regular file", name)
		}
	}
	return nil
}

func liftContextCluster(kube *guard.Kubeconfig, contextName string) (string, error) {
	cluster := ""
	count := 0
	for _, item := range kube.Contexts {
		if item.Name == contextName {
			cluster = item.Context.Cluster
			count++
		}
	}
	if count != 1 || cluster == "" {
		return "", fmt.Errorf("destination context %s is missing or ambiguous in kubeconfig", contextName)
	}
	count = 0
	for _, item := range kube.Clusters {
		if item.Name == cluster && item.Cluster.Server != "" {
			count++
		}
	}
	if count != 1 {
		return "", fmt.Errorf("destination context %s has no unique cluster in kubeconfig", contextName)
	}
	return cluster, nil
}

func (a *App) liftClusterUID(ctx context.Context) (string, error) {
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", "kube-system", "-o", "json")
	if err != nil {
		return "", fmt.Errorf("cannot establish destination cluster identity (kube-system UID): %w", err)
	}
	var namespace struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &namespace) != nil || namespace.Kind != "Namespace" || namespace.Metadata.Name != "kube-system" || namespace.Metadata.UID == "" {
		return "", fmt.Errorf("destination kube-system namespace returned no valid cluster UID")
	}
	return namespace.Metadata.UID, nil
}

func (a *App) liftPrerequisites(ctx context.Context, namespace string) error {
	var gaps []error
	missing, err := a.missingLiftOrkaCRDs(ctx)
	if err != nil {
		return err // Without CRD discovery, other cluster reads may be untrustworthy.
	}
	if len(missing) > 0 {
		gaps = append(gaps, fmt.Errorf("missing Orka CRDs (%s); prepare destination with kmx orka install (or kmx aks up for a new cluster)", strings.Join(missing, ", ")))
	}
	controller, err := a.orkaControllerForLift(ctx)
	if err != nil {
		gaps = append(gaps, err)
	} else if _, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "rollout", "status", "deploy/"+controller, "--timeout=10s"); err != nil {
		gaps = append(gaps, fmt.Errorf("Orka controller is not Ready; prepare destination with kmx orka install: %w", err))
	}
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		gaps = append(gaps, fmt.Errorf("cannot check destination namespace %s: %w", namespace, err))
	} else {
		var ns struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string  `json:"name"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != namespace || ns.Metadata.DeletionTimestamp != nil {
			gaps = append(gaps, fmt.Errorf("destination namespace %s is missing or terminating; create it before lifting (kmx orka install prepares %s)", namespace, OrkaNamespace))
		}
	}
	return errors.Join(gaps...)
}

func (a *App) liftProviderBindings(ctx context.Context, namespace, provider string) (agentruntime.OrkaBindings, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "providers.core.orka.ai", provider, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return agentruntime.OrkaBindings{}, fmt.Errorf("cannot inspect destination Provider/%s: %w", provider, err)
	}
	var selected struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string  `json:"name"`
			Namespace         string  `json:"namespace"`
			UID               string  `json:"uid"`
			Generation        int64   `json:"generation"`
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Type    string `json:"type"`
			BaseURL string `json:"baseURL"`
			Azure   struct {
				DeploymentName string `json:"deploymentName"`
				APIVersion     string `json:"apiVersion"`
			} `json:"azure"`
			SecretRef struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"secretRef"`
		} `json:"spec"`
		Status struct {
			Ready      bool              `json:"ready"`
			Conditions []serverCondition `json:"conditions"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &selected) != nil || selected.Kind != "Provider" || selected.Metadata.Name != provider || selected.Metadata.Namespace != namespace || selected.Metadata.UID == "" || selected.Metadata.Generation < 1 || selected.Metadata.DeletionTimestamp != nil {
		return agentruntime.OrkaBindings{}, fmt.Errorf("destination Provider/%s is missing or has invalid identity; create a Ready Provider before lifting", provider)
	}
	ready := false
	for _, condition := range selected.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == selected.Metadata.Generation {
			ready = true
		}
	}
	if !selected.Status.Ready || !ready {
		return agentruntime.OrkaBindings{}, fmt.Errorf("destination Provider/%s is not Ready for its current generation", provider)
	}
	// Render defaults an absent key to api-key for shorthand create. A lift
	// must not silently replace the selected Provider's actual reference.
	if selected.Spec.SecretRef.Key == "" {
		return agentruntime.OrkaBindings{}, fmt.Errorf("destination Provider/%s has no explicit Secret reference key", provider)
	}
	bindings := agentruntime.OrkaBindings{Namespace: namespace, Provider: agentruntime.OrkaProviderBindings{Type: selected.Spec.Type, BaseURL: selected.Spec.BaseURL, Azure: agentruntime.OrkaAzureBindings{DeploymentName: selected.Spec.Azure.DeploymentName, APIVersion: selected.Spec.Azure.APIVersion}, SecretRef: agentruntime.OrkaSecretRefBindings{Name: selected.Spec.SecretRef.Name, Key: selected.Spec.SecretRef.Key}}}
	if err := agentruntime.ValidateOrkaBindings(bindings); err != nil {
		return agentruntime.OrkaBindings{}, fmt.Errorf("destination Provider/%s has invalid bindings (type, endpoint or Secret reference): %w", provider, err)
	}
	return bindings, nil
}

func (a *App) liftToolsAvailable(ctx context.Context, namespace string, bundle *scaffold.OrkaBundle) error {
	tools, _ := bundle.Agent["spec"].(map[string]any)["tools"].([]any)
	worker := ""
	checked := map[string]bool{}
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" {
			return fmt.Errorf("rendered Agent has invalid Tool reference")
		}
		if tool["enabled"] == false {
			continue
		}
		raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "tools.core.orka.ai", name, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return fmt.Errorf("cannot inspect Tool/%s in namespace %s: %w", name, namespace, err)
		}
		var existing struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string  `json:"name"`
				Namespace         string  `json:"namespace"`
				Generation        int64   `json:"generation"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				HTTP json.RawMessage `json:"http"`
			} `json:"spec"`
			Status struct {
				Conditions []serverCondition `json:"conditions"`
			} `json:"status"`
		}
		if json.Unmarshal(raw, &existing) != nil || existing.Kind != "Tool" || existing.Metadata.Name != name || existing.Metadata.Namespace != namespace || existing.Metadata.Generation < 1 || existing.Metadata.DeletionTimestamp != nil {
			return fmt.Errorf("Tool/%s is missing in namespace %s; provision the Tool before lifting", name, namespace)
		}
		available := false
		for _, condition := range existing.Status.Conditions {
			if condition.Type == "Available" && condition.Status == "True" && condition.ObservedGeneration == existing.Metadata.Generation {
				available = true
			}
		}
		if !available {
			return fmt.Errorf("Tool/%s in namespace %s is not Available; repair it before lifting (the quickstart Kubernetes tool is not Available on Orka v0.1.3)", name, namespace)
		}
		if len(existing.Spec.HTTP) == 0 {
			continue // Non-HTTP Tools have no outbound HTTP policy.
		}
		var http struct {
			PolicyRef json.RawMessage `json:"outboundAccessPolicyRef"`
		}
		if json.Unmarshal(existing.Spec.HTTP, &http) != nil {
			return fmt.Errorf("Tool/%s in namespace %s has invalid HTTP policy reference", name, namespace)
		}
		if len(http.PolicyRef) == 0 {
			continue
		}
		var ref struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(http.PolicyRef, &ref) != nil || scaffold.ValidateObjectName(ref.Name) != nil {
			return fmt.Errorf("Tool/%s in namespace %s has invalid outbound policy reference", name, namespace)
		}
		key := namespace + "/" + ref.Name
		if checked[key] {
			continue
		}
		if worker == "" {
			worker, err = a.orkaAIWorkerAccount(ctx)
			if err != nil {
				return fmt.Errorf("Tool/%s in namespace %s: cannot discover Orka AI worker for policy %s: %w", name, namespace, ref.Name, err)
			}
		}
		if err := a.orkaWorkerCanGetPolicy(ctx, worker, namespace, ref.Name); err != nil {
			var denied *policyPermissionDenied
			if errors.As(err, &denied) {
				return fmt.Errorf("Tool/%s in namespace %s: %w", name, namespace, &policyPermissionDenied{Tool: name, Worker: denied.Worker, Namespace: denied.Namespace, Policy: denied.Policy})
			}
			return fmt.Errorf("Tool/%s in namespace %s: %w", name, namespace, err)
		}
		checked[key] = true
	}
	return nil
}

// liftAllowedAgentsPresent checks the destination namespace, not the bundle's
// creation target. A missing helper must be provisioned before the coordinator.
func (a *App) liftAllowedAgentsPresent(ctx context.Context, namespace string, bundle *scaffold.OrkaBundle) error {
	spec, _ := bundle.Agent["spec"].(map[string]any)
	coordination, _ := spec["coordination"].(map[string]any)
	if coordination["enabled"] != true {
		return nil
	}
	self := orkaObjectName(bundle.Agent)
	refs, _ := coordination["allowedAgents"].([]any)
	var missing []string
	seen := make(map[string]bool)
	for _, entry := range refs {
		ref, _ := entry.(map[string]any)
		name, _ := ref["name"].(string)
		if name == "" {
			return fmt.Errorf("rendered Agent has invalid allowed Agent reference")
		}
		if name == self || seen[name] {
			continue
		}
		seen[name] = true
		raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "agents.core.orka.ai", name, "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return fmt.Errorf("cannot inspect Agent/%s in namespace %s: %w", name, namespace, err)
		}
		var existing struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string  `json:"name"`
				Namespace         string  `json:"namespace"`
				Generation        int64   `json:"generation"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &existing) != nil || existing.Kind != "Agent" || existing.Metadata.Name != name || existing.Metadata.Namespace != namespace || existing.Metadata.Generation < 1 || existing.Metadata.DeletionTimestamp != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("allowed Agents missing in namespace %s: %s; provision them before lifting", namespace, strings.Join(missing, ", "))
	}
	return nil
}

func (a *App) printLiftDecision(check orkaReconcileCheck) {
	if check.markerRefresh {
		a.notef("%s/%s: reused; ownership markers would be refreshed", check.id.Kind, check.id.Name)
		return
	}
	if check.outcome == agentruntime.ResourceUpdated {
		a.notef("%s/%s: updated; differing rendered fields: %s", check.id.Kind, check.id.Name, strings.Join(orkaChangedFields(check.existing, check.candidate), ", "))
		return
	}
	a.notef("%s/%s: %s", check.id.Kind, check.id.Name, check.outcome)
}

func bundleLiftSelectionPath(bundle string) (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	canonical, err := resolveOrkaPath(bundle)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(canonical))
	return filepath.Join(dir, "bundle-lift", fmt.Sprintf("%x.json", key)), nil
}

func loadBundleLiftSelection(path string) (bundleLiftSelection, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return bundleLiftSelection{}, nil
	}
	if err != nil {
		return bundleLiftSelection{}, fmt.Errorf("read remembered bundle target: %w", err)
	}
	var selection bundleLiftSelection
	if json.Unmarshal(raw, &selection) != nil || selection.Context == "" || selection.ClusterUID == "" || selection.Namespace == "" || selection.Inference == "" {
		return bundleLiftSelection{}, fmt.Errorf("ambiguous remembered bundle target; repair local lift state or specify a different bundle")
	}
	return selection, nil
}

func saveBundleLiftSelection(path string, selection bundleLiftSelection) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, append(raw, '\n'))
}

func checkLiftReceiptsDir(bundle string) error {
	info, err := os.Lstat(filepath.Join(bundle, "receipts"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("receipts must be a directory, not a link")
	}
	return nil
}

func writeLiftReceipt(bundle, clusterUID string, receipt bundleLiftReceipt) error {
	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	// Include UID: replacing a cluster behind an unchanged context must never
	// overwrite the receipt for the former physical destination.
	target := receipt.Receipt.Target
	key := sha256.Sum256([]byte(target.Context + "\x00" + target.Namespace + "\x00" + clusterUID))
	path := filepath.Join(dir, fmt.Sprintf("%x.json", key))
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, append(raw, '\n'))
}

// liftAgentUncommittedReason identifies which part of the exact-file Git check
// failed without printing Git stderr or any file content.
func liftAgentUncommittedReason(ctx context.Context, bundle string) string {
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rootRaw, err := exec.CommandContext(gitCtx, "git", "-C", bundle, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "not in a Git repository"
	}
	root := strings.TrimSpace(string(rootRaw))
	rel, err := filepath.Rel(root, filepath.Join(bundle, "agent.yaml"))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "outside the Git repository"
	}
	if _, err := exec.CommandContext(gitCtx, "git", "-C", root, "show", "HEAD:"+filepath.ToSlash(rel)).Output(); err != nil {
		return "not tracked in HEAD"
	}
	if err := exec.CommandContext(gitCtx, "git", "-C", root, "diff", "--cached", "--quiet", "HEAD", "--", rel).Run(); err != nil {
		return "staged changes differ from HEAD"
	}
	committed, err := exec.CommandContext(gitCtx, "git", "-C", root, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
	portable, readErr := os.ReadFile(filepath.Join(bundle, "agent.yaml"))
	if err == nil && readErr == nil && !bytes.Equal(portable, committed) {
		return "working file differs from HEAD"
	}
	return "Git revision could not be verified"
}

// Determine provenance only from the exact portable file. Unrelated untracked
// paths and changes outside agent.yaml cannot turn a clean revision dirty.
func liftAgentCommit(ctx context.Context, bundle, portableDigest string) string {
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rootRaw, err := exec.CommandContext(gitCtx, "git", "-C", bundle, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "uncommitted"
	}
	root := strings.TrimSpace(string(rootRaw))
	rel, err := filepath.Rel(root, filepath.Join(bundle, "agent.yaml"))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "uncommitted"
	}
	if exec.CommandContext(gitCtx, "git", "-C", root, "ls-files", "--error-unmatch", "--", rel).Run() != nil ||
		exec.CommandContext(gitCtx, "git", "-C", root, "diff", "--cached", "--quiet", "HEAD", "--", rel).Run() != nil {
		return "uncommitted"
	}
	// Git's skip-worktree/assume-unchanged hints can hide working-tree edits
	// from diff. Compare HEAD's blob directly to the exact bytes Render reads.
	committed, err := exec.CommandContext(gitCtx, "git", "-C", root, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return "uncommitted"
	}
	portable, err := os.ReadFile(filepath.Join(bundle, "agent.yaml"))
	if err != nil || !bytes.Equal(portable, committed) || agentruntime.PortableBundleDigest(committed) != portableDigest {
		return "uncommitted"
	}
	commit, err := exec.CommandContext(gitCtx, "git", "-C", root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "uncommitted"
	}
	return strings.TrimSpace(string(commit))
}
