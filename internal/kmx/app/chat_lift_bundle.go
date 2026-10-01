package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"golang.org/x/term"
)

// A bundle lift intentionally does not use the live-source Provider, Foundry
// provisioning or createOrkaOnline. The shared CLI operation owns rendering,
// admission, versioned reconciliation, receipts and remembered selections.
func (b *orkaChatBackend) liftBundledAgentTo(ctx context.Context, renderer *chatRenderer, target chatLiftTarget, dir string) error {
	if _, _, _, err := readBundlePortableAgent(dir); err != nil {
		return err
	}
	worker := *b.app
	cfg := *b.app.Cfg
	runner := *b.app.Run
	worker.Cfg, worker.Run = &cfg, &runner
	runner.Context = ctx
	worker.guarded = false
	cfg.KubeContext, cfg.ContextSource = target.Context, config.SourceFlag
	worker.Out, worker.Err, worker.Stdin = io.Discard, io.Discard, nil
	if target.DefaultKubeconfig {
		clean := make([]string, 0, len(runner.Env))
		for _, value := range runner.Env {
			if !strings.HasPrefix(value, "KUBECONFIG=") {
				clean = append(clean, value)
			}
		}
		runner.Env = clean
	}
	if target.Kubeconfig != "" {
		located := appAtAgentLocation(&worker, agentLocation{Context: target.Context, Kubeconfig: target.Kubeconfig})
		runner.Env = located.Run.Env
	}
	if target.Subscription != "" {
		temp, err := os.MkdirTemp("", "kmx-bundle-lift-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		path := filepath.Join(temp, "kubeconfig")
		if _, err := b.liftAzureFetch(ctx, aksCredentialsArgs(target, path)...); err != nil {
			return err
		}
		located := appAtAgentLocation(&worker, agentLocation{Context: target.Context, Kubeconfig: path})
		runner.Env = located.Run.Env
	}
	namespace := b.namespace
	if namespace == "" {
		namespace = OrkaNamespace
	}
	selectedUID, err := worker.liftClusterUID(ctx)
	if err != nil {
		return err
	}
	name, _, digest, err := readBundlePortableAgent(dir)
	if err != nil {
		return err
	}
	if _, err := loadBundleLiftPolicy(dir); err != nil {
		return err
	}
	required, failed := evaluateBundleLiftGate(dir, name, digest, bundleGateTarget{ClusterUID: selectedUID, Namespace: namespace}, "")
	if required && failed != "" {
		return fmt.Errorf("bundle lift evaluation gate refused before target preparation: %s", failed)
	}
	// Do not rely on the CLI's remembered target: this review authorizes exactly
	// the context, namespace, and Ready inference Provider selected here.
	preparedOrka, preparedResultAccess, preparedTool := false, false, false
	for {
		if err := confirmBundleTargetUID(ctx, &worker, selectedUID); err != nil {
			return err
		}
		missing, err := worker.missingLiftOrkaCRDs(ctx)
		if err != nil {
			return err
		}
		if len(missing) != 0 {
			if preparedOrka {
				return fmt.Errorf("target %s still lacks Orka CRDs after preparation: %s", target.Context, strings.Join(missing, ", "))
			}
			if err := b.prepareBundleLiftOrka(ctx, &worker, selectedUID, strings.Join(missing, ", ")); err != nil {
				return liftPreparationError(err)
			}
			preparedOrka = true
			continue
		}
		controller, err := worker.orkaControllerForLift(ctx)
		if err == nil {
			_, err = worker.orkaCapture(ctx, nil, "-n", OrkaNamespace, "rollout", "status", "deploy/"+controller, "--timeout=10s")
		}
		if err != nil {
			if preparedOrka || (!strings.Contains(err.Error(), "no Orka controller Deployment") && !strings.Contains(err.Error(), "requested Kubernetes resource not found")) {
				return fmt.Errorf("target %s: Orka controller unavailable: %w", target.Context, err)
			}
			if err := b.prepareBundleLiftOrka(ctx, &worker, selectedUID, "Orka controller unavailable: "+err.Error()); err != nil {
				return liftPreparationError(err)
			}
			preparedOrka = true
			continue
		}
		resultReader, err := bundleResultReaderPresent(ctx, &worker, namespace)
		if err != nil {
			return err
		}
		if !resultReader {
			if namespace != OrkaNamespace {
				return fmt.Errorf("target %s needs result ServiceAccount/%s and Task-get Role/RoleBinding in namespace %s before lift; prepare them and retry /lift", target.Context, orkaResultAccount, namespace)
			}
			if preparedResultAccess {
				return fmt.Errorf("target %s still lacks result ServiceAccount/%s after preparation", target.Context, orkaResultAccount)
			}
			if err := b.confirmLiftAction(ctx, "Prepare target "+target.Context+": chat needs a result ServiceAccount and Task-get grant before connecting.", "Prepare target: install Task result reader RBAC"); err != nil {
				return liftPreparationError(err)
			}
			if err := b.guardBundlePreparation(ctx, &worker, selectedUID); err != nil {
				return err
			}
			if err := b.runLiftDeployment(ctx, &worker, "Prepare result access", []string{"Install result reader RBAC"}, func(w *App) error {
				if err := confirmBundleTargetUID(w.operationContext(), w, selectedUID); err != nil {
					return err
				}
				return w.runPhase(phase{current: 1, total: 1, name: "Install result reader RBAC"}, w.orkaResultReader)
			}); err != nil {
				return err
			}
			preparedResultAccess = true
			continue
		}
		providers, err := b.bundleLiftProviders(ctx, &worker, namespace)
		if err != nil {
			return err
		}
		if len(providers) == 0 {
			return fmt.Errorf("no eligible Ready Provider in target %s namespace %s (excluding the Agent's own Provider/%s); provision and wait for a separate Provider to be Ready at its current generation, then retry /lift; Foundry is not available for bundle lift", target.Context, namespace, b.agent)
		}
		items := make([]chatPickerItem, len(providers))
		for i, name := range providers {
			items[i] = chatPickerItem{name: name}
		}
		index, ok, err := b.liftPick(ctx, "Ready inference Provider on "+target.Context, items)
		if err != nil || !ok {
			return err
		}
		_, _, plannedDigest, err := readBundlePortableAgent(dir)
		if err != nil {
			return err
		}
		opt := LiftAgentBundleOptions{BundleDir: dir, ToContext: target.Context, ToNamespace: namespace, Inference: "provider:" + providers[index], Plan: true}
		if err := confirmBundleTargetUID(ctx, &worker, selectedUID); err != nil {
			return err
		}
		reviewUID := selectedUID
		notes := &bundlePlanNotes{buffer: orkaBoundedBuffer{remaining: 64 << 10}}
		worker.Err = notes // LiftAgentBundle sends its safe decisions through notef (Err), not Out.
		err = worker.LiftAgentBundle(opt)
		if err != nil {
			renderer.operation("LIFT", "", colorBlue, "Bundle plan could not proceed: "+err.Error())
			if !preparedTool && b.bundleLiftPreparable(namespace, err) {
				if prepErr := b.prepareBundleLiftTarget(ctx, &worker, selectedUID, namespace, err); prepErr != nil {
					return liftPreparationError(prepErr)
				}
				preparedTool = true
				continue // target state may have changed; re-discover and re-plan.
			}
			return fmt.Errorf("bundle lift plan failed (no deployment): %w", err)
		}
		if err := confirmBundleTargetUID(ctx, &worker, reviewUID); err != nil {
			return err
		}
		if !notes.complete() {
			return fmt.Errorf("bundle plan output exceeded review limit; nothing deployed (use %s --plan to inspect it)", bundleLiftCommand(dir, target.Context, namespace, providers[index]))
		}
		// Notes are safe, bounded CLI output: resource outcomes and field paths,
		// never rendered values or the selected Provider's Secret contents.
		review := strings.TrimSpace(notes.buffer.buffer.String())
		title := "Review bundle lift to " + target.Context + " / " + namespace + "\n" + review
		if !bundlePlanFitsTerminal(b.app.Out, title) {
			return fmt.Errorf("bundle plan is too long for this terminal; enlarge it and retry /lift, or review with %s --plan", bundleLiftCommand(dir, target.Context, namespace, providers[index]))
		}
		renderer.operation("LIFT PLAN", "", colorBlue, review)
		index, ok, err = b.liftAction(ctx, title, []chatPickerItem{{name: "Cancel"}, {name: "Deploy bundle to " + target.Context}})
		if err != nil || !ok || index == 0 {
			return err
		}
		if !bundlePlanFitsTerminal(b.app.Out, title) {
			return fmt.Errorf("terminal no longer fits the full bundle plan; nothing deployed")
		}
		if err := checkBundlePlanRevision(dir, plannedDigest); err != nil {
			return err
		}
		// The guard's context-bound confirmation is set only after this exact review
		// was accepted. Deployment itself cannot install Orka, Tools or inference.
		cfg.Confirm = target.Context
		opt.Plan = false
		worker.Err = io.Discard
		renderer.operation("LIFT", "", colorBlue, "Deploying bundle to "+target.Context+"…")
		if err := b.runLiftDeployment(ctx, &worker, "Deploy bundle", []string{"Reconcile bundle"}, func(deploy *App) error {
			if err := checkBundlePlanRevision(dir, plannedDigest); err != nil {
				return err
			}
			if err := confirmBundleTargetUID(deploy.operationContext(), deploy, reviewUID); err != nil {
				return err
			}
			if err := confirmBundlePlan(deploy.operationContext(), deploy, opt, review); err != nil {
				return err
			}
			return deploy.runPhase(phase{current: 1, total: 1, name: "Reconcile bundle"}, func() error {
				if err := confirmBundleTargetUID(deploy.operationContext(), deploy, reviewUID); err != nil {
					return err
				}
				if err := confirmBundleResultAccess(deploy.operationContext(), deploy, namespace); err != nil {
					return err
				}
				return deploy.LiftAgentBundle(opt)
			})
		}); err != nil {
			return bundleDeployError(err)
		}
		return b.finishBundleLift(ctx, renderer, &worker, target, opt)
	}
}

func bundleDeployError(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("bundle deployment cancelled; Provider or Agent may have changed on the destination; inspect resources and receipt before retrying")
	}
	return err
}

type bundlePlanNotes struct {
	buffer   orkaBoundedBuffer
	overflow bool
}

func (n *bundlePlanNotes) Write(p []byte) (int, error) {
	written, err := n.buffer.Write(p)
	if err != nil {
		n.overflow = true
	}
	return written, err
}

func (n *bundlePlanNotes) complete() bool { return !n.overflow }

// The destination can change while a review picker is open. A fresh read-only
// plan must still describe the same decisions before any mutation is attempted.
func confirmBundleResultAccess(ctx context.Context, worker *App, namespace string) error {
	ready, err := bundleResultReaderPresent(ctx, worker, namespace)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("destination result access changed since review; prepare the Task result reader and rerun /lift")
	}
	return nil
}

func confirmBundleTargetUID(ctx context.Context, worker *App, reviewed string) error {
	uid, err := worker.liftClusterUID(ctx)
	if err != nil {
		return fmt.Errorf("cannot verify reviewed destination identity: %w", err)
	}
	if uid != reviewed {
		return fmt.Errorf("selected context now identifies a different cluster; rerun /lift for a new plan")
	}
	return nil
}

func confirmBundlePlan(ctx context.Context, worker *App, opt LiftAgentBundleOptions, reviewed string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notes := &bundlePlanNotes{buffer: orkaBoundedBuffer{remaining: 64 << 10}}
	check := *worker
	check.Err = notes
	opt.Plan = true
	if err := check.LiftAgentBundle(opt); err != nil {
		return fmt.Errorf("destination changed since bundle review; rerun /lift for a new plan: %w", err)
	}
	if !notes.complete() || strings.TrimSpace(notes.buffer.buffer.String()) != reviewed {
		return fmt.Errorf("destination changed since bundle review; rerun /lift for a new plan")
	}
	return nil
}

func checkBundlePlanRevision(dir, digest string) error {
	_, _, current, err := readBundlePortableAgent(dir)
	if err != nil {
		return fmt.Errorf("bundle changed during review; rerun /lift for a new plan: %w", err)
	}
	if current != digest {
		return fmt.Errorf("bundle changed during review; rerun /lift for a new plan")
	}
	return nil
}

func bundlePlanFitsTerminal(out io.Writer, title string) bool {
	width, height := 0, 0
	if file, ok := out.(*os.File); ok && isInteractiveTerminal(file) {
		width, height, _ = term.GetSize(int(file.Fd()))
	}
	return bundlePlanTitleFits(title, width, height)
}

func bundlePlanTitleFits(title string, width, height int) bool {
	return chatPickerTitleFits("LIFT · "+title, width, height, true)
}

func bundleResultReaderPresent(ctx context.Context, worker *App, namespace string) (bool, error) {
	raw, err := worker.orkaCapture(ctx, nil, "-n", namespace, "get", "serviceaccount", orkaResultAccount, "--ignore-not-found=true", "-o", "name")
	if err != nil {
		return false, fmt.Errorf("cannot inspect result ServiceAccount in %s: %w", namespace, err)
	}
	if strings.TrimSpace(string(raw)) != "serviceaccount/"+orkaResultAccount {
		return false, nil
	}
	raw, err = worker.orkaCapture(ctx, nil, "-n", namespace, "get", "role", orkaResultAccount, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return false, fmt.Errorf("cannot inspect result Role in %s: %w", namespace, err)
	}
	if len(raw) == 0 {
		return false, nil
	}
	var role struct {
		Kind     string
		Metadata struct{ Name, Namespace string }
		Rules    []struct {
			APIGroups, Resources, Verbs, ResourceNames []string
		}
	}
	if err := json.Unmarshal(raw, &role); err != nil {
		return false, fmt.Errorf("invalid result Role in %s: %w", namespace, err)
	}
	if role.Kind != "Role" || role.Metadata.Name != orkaResultAccount || role.Metadata.Namespace != namespace || len(role.Rules) != 1 {
		return false, nil
	}
	rule := role.Rules[0]
	if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "core.orka.ai" || len(rule.Resources) != 1 || rule.Resources[0] != "tasks" || len(rule.Verbs) != 1 || rule.Verbs[0] != "get" || len(rule.ResourceNames) != 0 {
		return false, nil
	}
	raw, err = worker.orkaCapture(ctx, nil, "-n", namespace, "get", "rolebinding", orkaResultAccount, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return false, fmt.Errorf("cannot inspect result RoleBinding in %s: %w", namespace, err)
	}
	if len(raw) == 0 {
		return false, nil
	}
	var binding struct {
		Kind     string
		Metadata struct{ Name, Namespace string }
		RoleRef  struct{ APIGroup, Kind, Name string }
		Subjects []struct{ Kind, Name, Namespace string }
	}
	if err := json.Unmarshal(raw, &binding); err != nil {
		return false, fmt.Errorf("invalid result RoleBinding in %s: %w", namespace, err)
	}
	return binding.Kind == "RoleBinding" && binding.Metadata.Name == orkaResultAccount && binding.Metadata.Namespace == namespace &&
		binding.RoleRef.APIGroup == "rbac.authorization.k8s.io" && binding.RoleRef.Kind == "Role" && binding.RoleRef.Name == orkaResultAccount &&
		len(binding.Subjects) == 1 && binding.Subjects[0].Kind == "ServiceAccount" && binding.Subjects[0].Name == orkaResultAccount && binding.Subjects[0].Namespace == namespace, nil
}

func (b *orkaChatBackend) bundleLiftProviders(ctx context.Context, worker *App, namespace string) ([]string, error) {
	raw, err := worker.orkaCapture(ctx, nil, "-n", namespace, "get", "providers.core.orka.ai", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("target %s: cannot list Ready Providers in namespace %s: %w", worker.Cfg.KubeContext, namespace, err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string  `json:"name"`
				Generation        int64   `json:"generation"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Ready      bool              `json:"ready"`
				Conditions []serverCondition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("invalid target Provider list: %w", err)
	}
	var names []string
	for _, item := range list.Items {
		if item.Metadata.Name == b.agent || item.Metadata.Name == "" || item.Metadata.Generation < 1 || item.Metadata.DeletionTimestamp != nil || !item.Status.Ready {
			continue
		}
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == item.Metadata.Generation {
				names = append(names, item.Metadata.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// Only a non-allowing review for the fixed quickstart identity can be repaired
// by reapplying its named grant. API errors and other policy failures cannot.
func quickstartPolicyRepairable(namespace string, err error) bool {
	var denied *policyPermissionDenied
	return errors.As(err, &denied) && denied.Tool == quickstartK8sTool &&
		denied.Namespace == OrkaNamespace && namespace == OrkaNamespace && denied.Policy == quickstartK8sToolPolicy
}

// Only preparation has a separate confirmation. An arbitrary missing Tool or
// a custom namespace cannot be repaired by the fixed-namespace quickstart.
func (b *orkaChatBackend) bundleLiftPreparable(namespace string, planErr error) bool {
	if quickstartPolicyRepairable(namespace, planErr) {
		return true
	}
	if strings.Contains(planErr.Error(), "missing Orka CRDs") {
		return true
	}
	prefix := "Tool/" + quickstartK8sTool + " in namespace " + namespace
	return namespace == OrkaNamespace && (strings.HasPrefix(planErr.Error(), prefix+" is not Available") || strings.HasPrefix(planErr.Error(), "Tool/"+quickstartK8sTool+" is missing in namespace "+namespace))
}

// A preparation confirmation authorizes one pinned destination, not ambient
// kubeconfig or the following bundle deployment's separate confirmation.
func (b *orkaChatBackend) guardBundlePreparation(ctx context.Context, worker *App, selectedUID string) error {
	if err := confirmBundleTargetUID(ctx, worker, selectedUID); err != nil {
		return err
	}
	raw, err := worker.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot inspect preparation target: %w", err)
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return fmt.Errorf("cannot decode preparation target context")
	}
	if err := guard.CheckContext(ctx, kube, guard.Request{
		Action: "prepare Orka and tools on the selected destination", Context: worker.Cfg.KubeContext,
		Source: worker.Cfg.ContextSource, Namespaces: OrkaNamespace, Confirm: worker.Cfg.KubeContext,
		Command: "kmx orka install",
	}, worker.Err, nil); err != nil {
		return err
	}
	if err := confirmBundleTargetUID(ctx, worker, selectedUID); err != nil {
		return err
	}
	worker.guarded = true
	return nil
}

func (b *orkaChatBackend) prepareBundleLiftOrka(ctx context.Context, worker *App, selectedUID, missing string) error {
	if err := b.confirmLiftAction(ctx, "Prepare target "+worker.Cfg.KubeContext+": missing Orka CRDs ("+missing+"). Preparation may leave resources installed even if lift is cancelled.", "Prepare target: install Orka "+OrkaVersion); err != nil {
		return err
	}
	if err := b.guardBundlePreparation(ctx, worker, selectedUID); err != nil {
		return err
	}
	return b.runLiftDeployment(ctx, worker, "Install Orka", []string{"Fetch the pinned chart", "Apply chart CRDs and wait", "Install harness-v2 and result reader"}, func(w *App) error {
		if err := confirmBundleTargetUID(w.operationContext(), w, selectedUID); err != nil {
			return err
		}
		return w.OrkaInstall(OrkaOptions{Provider: "-"})
	})
}

func (b *orkaChatBackend) prepareBundleLiftTarget(ctx context.Context, worker *App, selectedUID, namespace string, planErr error) error {
	if strings.Contains(planErr.Error(), "Orka controller") || strings.Contains(planErr.Error(), "Orka CRDs") {
		return b.prepareBundleLiftOrka(ctx, worker, selectedUID, planErr.Error())
	}
	if namespace != OrkaNamespace {
		return fmt.Errorf("Tool/%s must be prepared in destination namespace %s before retrying /lift", quickstartK8sTool, namespace)
	}
	if err := b.confirmLiftAction(ctx, "Prepare target "+worker.Cfg.KubeContext+": Kubernetes inventory Tool or its worker policy grant needs repair. Preparation may leave resources installed even if lift is cancelled.", "Prepare target: install read-only Kubernetes Tool and RBAC"); err != nil {
		return err
	}
	if err := b.guardBundlePreparation(ctx, worker, selectedUID); err != nil {
		return err
	}
	return b.runLiftDeployment(ctx, worker, "Prepare Kubernetes Tool", []string{"Install tool server and wait Ready"}, func(w *App) error {
		if err := confirmBundleTargetUID(w.operationContext(), w, selectedUID); err != nil {
			return err
		}
		return w.runPhase(phase{current: 1, total: 1, name: "Install tool server and wait Ready"}, w.installQuickstartK8sTool)
	})
}

func (b *orkaChatBackend) finishBundleLift(ctx context.Context, renderer *chatRenderer, worker *App, target chatLiftTarget, opt LiftAgentBundleOptions) error {
	if _, err := rememberAgentLocation(ctx, b.app, agentLocation{Agent: b.agent, Namespace: b.namespace, Context: b.app.Cfg.KubeContext}); err != nil {
		return fmt.Errorf("Agent deployed, but source location could not be saved: %w", err)
	}
	location, err := rememberAgentLocation(ctx, worker, agentLocation{Agent: b.agent, Namespace: opt.ToNamespace, Context: target.Context, Cluster: target.Cluster, Subscription: target.Subscription, ResourceGroup: target.ResourceGroup})
	if err != nil {
		return fmt.Errorf("Agent deployed, but target location could not be saved: %w", err)
	}
	if err := b.connectAgentLocation(ctx, location); err != nil {
		return fmt.Errorf("Agent deployed and saved; connecting failed: %w", err)
	}
	renderer.operation("LIFT", "", colorGreen, "Connected to lifted Agent on "+target.Context+". Send a new message. Equivalent command: "+bundleLiftCommand(opt.BundleDir, target.Context, opt.ToNamespace, strings.TrimPrefix(opt.Inference, "provider:")))
	return nil
}

func bundleLiftCommand(dir, contextName, namespace, provider string) string {
	args := []string{"kmx", "agent", "lift", dir, "--to-context", contextName}
	if namespace != "" && namespace != OrkaNamespace {
		args = append(args, "--to-namespace", namespace)
	}
	args = append(args, "--inference", "provider:"+provider)
	for i := range args {
		args[i] = shellArg(args[i])
	}
	return strings.Join(args, " ")
}
