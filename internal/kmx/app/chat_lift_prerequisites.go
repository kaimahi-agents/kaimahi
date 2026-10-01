package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errLiftBack = errors.New("lift selection cancelled")

func liftPreparationError(err error) error {
	if errors.Is(err, errLiftBack) {
		return nil
	}
	return err
}

func (b *orkaChatBackend) confirmLiftAction(ctx context.Context, title, action string) error {
	i, ok, err := b.liftAction(ctx, title, []chatPickerItem{{name: "Cancel"}, {name: action}})
	if err != nil {
		return err
	}
	if !ok || i == 0 {
		return errLiftBack
	}
	return nil
}

func (a *App) missingLiftOrkaCRDs(ctx context.Context) ([]string, error) {
	var missing []string
	for _, kind := range []string{"agents", "providers", "tasks", "tools"} {
		name := kind + ".core.orka.ai"
		raw, err := a.orkaCapture(ctx, nil, "get", "crd", name, "--ignore-not-found=true", "-o", "name")
		if err != nil {
			return nil, fmt.Errorf("target %s: cannot check Orka CRDs; verify cluster connectivity and permission to read CRDs: %w", a.Cfg.KubeContext, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func (b *orkaChatBackend) prepareLiftOrka(ctx context.Context, target *App, r *chatRenderer) error {
	r.operation("LIFT", "", colorBlue, "Checking Orka on target "+target.Cfg.KubeContext+"…")
	missing, err := target.missingLiftOrkaCRDs(ctx)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := b.confirmLiftAction(ctx, "Orka missing on "+target.Cfg.KubeContext+"\n"+strings.Join(missing, ", "), "Install Orka "+OrkaVersion+" on target"); err != nil {
			return err
		}
		r.operation("LIFT", "", colorBlue, "Installing Orka on "+target.Cfg.KubeContext+"…")
		if err := b.installLiftOrkaPane(ctx, target); err != nil {
			return fmt.Errorf("target %s: Orka installation failed: %w", target.Cfg.KubeContext, err)
		}
		missing, err = target.missingLiftOrkaCRDs(ctx)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("target %s: Orka installation is incomplete", target.Cfg.KubeContext)
		}
	}
	// Confirm a running controller as well as schema presence. Existing v0.1.3
	// targets remain usable; only the install path refuses to upgrade them.
	controller, err := target.orkaControllerForLift(ctx)
	if err != nil {
		return err
	}
	if _, err := target.orkaCapture(ctx, nil, "-n", OrkaNamespace, "rollout", "status", "deploy/"+controller, "--timeout=10s"); err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.confirmLiftAction(ctx, "Orka controller unavailable on "+target.Cfg.KubeContext, "Install/repair Orka "+OrkaVersion); err != nil {
			return err
		}
		if err := b.installLiftOrkaPane(ctx, target); err != nil {
			return fmt.Errorf("target %s: Orka repair failed: %w", target.Cfg.KubeContext, err)
		}
		if _, err := target.orkaCapture(ctx, nil, "-n", OrkaNamespace, "rollout", "status", "deploy/"+controller, "--timeout=10s"); err != nil {
			return fmt.Errorf("target %s: Orka controller still unavailable after attempted repair: %w", target.Cfg.KubeContext, err)
		}
	}
	if _, err := target.orkaCapture(ctx, nil, "get", "namespace", b.namespace, "-o", "name"); err != nil {
		return fmt.Errorf("target %s: namespace %s is unavailable: %w", target.Cfg.KubeContext, b.namespace, err)
	}
	return nil
}

type liftProvider struct {
	Metadata struct{ Name string }
	Spec     map[string]any
	Status   struct{ Ready bool }
}

func useLiftProvider(bundle *scaffold.OrkaBundle, spec map[string]any) error {
	// Clone the remote configuration, retaining a new Provider identity for this
	// Agent and the existing target Secret reference; never copy source keys.
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	var copySpec map[string]any
	if err := json.Unmarshal(raw, &copySpec); err != nil {
		return err
	}
	secret, _ := copySpec["secretRef"].(map[string]any)
	name, ok := secret["name"].(string)
	if !ok {
		return fmt.Errorf("selected inference has no Secret reference")
	}
	if err := validateAzureLiftModel(bundle.Agent, copySpec); err != nil {
		return err
	}
	bundle.Provider["spec"] = copySpec
	bundle.Secret["metadata"].(map[string]any)["name"] = name
	return bundle.Validate()
}

func validateAzureLiftModel(agent map[string]any, provider map[string]any) error {
	if provider["type"] != "azure-openai" {
		return nil
	}
	azure, _ := provider["azure"].(map[string]any)
	deployment, _ := azure["deploymentName"].(string)
	model, _ := provider["defaultModel"].(string)
	if agentSpec, ok := agent["spec"].(map[string]any); ok {
		if override, ok := agentSpec["model"].(map[string]any); ok {
			if name, ok := override["name"].(string); ok && name != "" {
				model = name
			}
		}
	}
	baseURL, _ := provider["baseURL"].(string)
	return scaffold.ValidateOrkaProvider("azure-openai", model, baseURL, deployment, "")
}

func (b *orkaChatBackend) selectLiftInference(ctx context.Context, target *App, cluster chatLiftTarget, bundle *scaffold.OrkaBundle, r *chatRenderer) error {
	r.operation("LIFT", "", colorBlue, "Discovering inference configured on "+target.Cfg.KubeContext+"…")
	raw, err := target.orkaCapture(ctx, nil, "-n", b.namespace, "get", "providers.core.orka.ai", "-o", "json")
	if err != nil {
		return fmt.Errorf("target %s: cannot list inference Providers: %w", target.Cfg.KubeContext, err)
	}
	var list struct{ Items []liftProvider }
	if err = json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var providers []liftProvider
	var items []chatPickerItem
	for _, p := range list.Items {
		if !p.Status.Ready {
			continue
		}
		providers = append(providers, p)
		items = append(items, chatPickerItem{name: p.Metadata.Name, detail: fmt.Sprintf("%v · %v", p.Spec["defaultModel"], p.Spec["baseURL"])})
	}
	items = append(items, chatPickerItem{name: "Azure Foundry", detail: "Select existing inference or create in the cluster resource group"})
	items = append(items, chatPickerItem{name: "Keep source Provider configuration", detail: "Target Secret and model endpoint must already exist"})
	i, ok, err := b.liftPick(ctx, "Inference on "+target.Cfg.KubeContext, items)
	if err != nil {
		return err
	}
	if !ok {
		return errLiftBack
	}
	if i < len(providers) {
		return useLiftProvider(bundle, providers[i].Spec)
	}
	if i == len(providers) {
		return b.configureLiftFoundry(ctx, target, cluster, bundle, r)
	}
	return nil
}

func (b *orkaChatBackend) prepareLiftTools(ctx context.Context, target *App, bundle *scaffold.OrkaBundle, r *chatRenderer) error {
	spec := bundle.Agent["spec"].(map[string]any)
	refs, _ := spec["tools"].([]any)
	for _, value := range refs {
		ref, _ := value.(map[string]any)
		if ref["name"] != quickstartK8sTool || ref["enabled"] == false {
			continue
		}
		raw, err := target.orkaCapture(ctx, nil, "-n", b.namespace, "get", "tools.core.orka.ai", quickstartK8sTool, "--ignore-not-found=true", "-o", "name")
		if err != nil {
			return fmt.Errorf("target %s: cannot check Kubernetes tool: %w", target.Cfg.KubeContext, err)
		}
		if strings.TrimSpace(string(raw)) != "" {
			return nil
		}
		if b.namespace != OrkaNamespace {
			return fmt.Errorf("install %s in target namespace %s before lifting", quickstartK8sTool, b.namespace)
		}
		if err := b.confirmLiftAction(ctx, "Kubernetes tool missing on "+target.Cfg.KubeContext, "Install read-only Kubernetes tool and RBAC"); err != nil {
			return err
		}
		r.operation("LIFT", "", colorBlue, "Installing Kubernetes tool on "+target.Cfg.KubeContext+"…")
		return b.runLiftDeployment(ctx, target, "Install Kubernetes tool", []string{"Install tool server and wait Ready"}, func(worker *App) error {
			return worker.runPhase(phase{current: 1, total: 1, name: "Install tool server and wait Ready"}, worker.installQuickstartK8sTool)
		})
	}
	return nil
}

func (b *orkaChatBackend) installLiftOrkaPane(ctx context.Context, target *App) error {
	return b.runLiftDeployment(ctx, target, "Install Orka", []string{"Fetch the pinned chart", "Apply chart CRDs and wait", "Install harness-v2 and result reader"}, func(worker *App) error { return worker.OrkaInstall(OrkaOptions{Provider: "-"}) })
}
