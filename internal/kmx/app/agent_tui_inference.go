package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Host sources store routing metadata only; Azure/Copilot retain their own login.
type consoleInferenceSource struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	Endpoint        string `json:"endpoint,omitempty"`
	Tenant          string `json:"tenant,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Secret          string `json:"secret,omitempty"`
	SecretKey       string `json:"secretKey,omitempty"`
	AzureDeployment string `json:"azureDeployment,omitempty"`
	AzureAPIVersion string `json:"azureAPIVersion,omitempty"`
	Subscription    string `json:"subscription,omitempty"`
	ResourceGroup   string `json:"resourceGroup,omitempty"`
	Account         string `json:"account,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
}

type consoleInferenceSnapshot struct {
	Version, Server string
	Model           map[string]any
	Sources         []consoleInferenceSource
}

func consoleInferencePath(env agentTUIEnvironment, agent agentTUIAgent) (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(env.Name+"\x00"+agent.key())))
	return filepath.Join(dir, "console-inference", id+".json"), nil
}

func loadConsoleInference(env agentTUIEnvironment, agent agentTUIAgent) (*consoleInferenceSource, error) {
	// Older console builds could save host overrides for remote agents. Ignore
	// them so remote execution always uses its cluster's configured credentials.
	if !env.Local {
		return nil, nil
	}
	path, err := consoleInferencePath(env, agent)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source consoleInferenceSource
	if err = json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("invalid saved inference source")
	}
	if source.Kind != "foundry" && source.Kind != "copilot" {
		return nil, fmt.Errorf("invalid saved host inference kind")
	}
	if err = source.validate(); err != nil {
		return nil, err
	}
	return &source, nil
}

func saveConsoleInference(env agentTUIEnvironment, agent agentTUIAgent, source *consoleInferenceSource) error {
	path, err := consoleInferencePath(env, agent)
	if err != nil {
		return err
	}
	if source == nil {
		err = os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !env.Local {
		return fmt.Errorf("remote environments cannot use host inference overrides")
	}
	if err = source.validate(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(source, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, raw)
}

// The console operates native Orka Agents only, so every source kind below is
// validated against Orka's requirements; there is no second runtime to branch
// on and no retired connector shape to accept.
func (s consoleInferenceSource) validate() error {
	if err := refuseWizardCredentials(s.Name, s.Model, s.Endpoint, s.Tenant, s.Provider, s.Secret, s.SecretKey, s.AzureDeployment, s.AzureAPIVersion); err != nil {
		return err
	}
	if strings.TrimSpace(s.Model) == "" {
		return fmt.Errorf("model or deployment is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("inference source name is required")
	}
	switch s.Kind {
	case "foundry-cluster":
		if err := scaffold.ValidateObjectName(s.Name); err != nil {
			return err
		}
		if err := (foundryChatConfig{Endpoint: s.Endpoint, Deployment: s.Model}).validate(); err != nil {
			return err
		}
		if s.Account != "" {
			if s.Subscription == "" || s.ResourceGroup == "" {
				return fmt.Errorf("Foundry Azure setup requires a subscription and resource group")
			}
			return nil
		}
		if err := scaffold.ValidateObjectName(s.Secret); err != nil {
			return fmt.Errorf("existing cluster Secret name is required: %w", err)
		}
		if s.SecretKey == "" || strings.ContainsAny(s.SecretKey, " /\r\n") {
			return fmt.Errorf("Secret key name is required")
		}
		return nil
	case "foundry":
		return (foundryChatConfig{Endpoint: s.Endpoint, Deployment: s.Model, Tenant: s.Tenant}).validate()
	case "copilot":
		return nil
	case "ollama", "apikey":
		if err := scaffold.ValidateObjectName(s.Name); err != nil {
			return err
		}
		u, err := url.Parse(s.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("use an HTTP(S) endpoint without credentials, query or fragment")
		}
		if s.Kind == "apikey" {
			if err := scaffold.ValidateObjectName(s.Secret); err != nil {
				return fmt.Errorf("existing Secret name is required: %w", err)
			}
			if s.SecretKey == "" || strings.ContainsAny(s.SecretKey, " /\r\n") {
				return fmt.Errorf("Secret key name is required")
			}
			if err := scaffold.ValidateOrkaProvider(s.Provider, s.Model, s.Endpoint, s.AzureDeployment, s.AzureAPIVersion); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown inference source")
	}
}

func (a *App) consoleAzureChoices(ctx context.Context, stage, subscription, group, account string) ([]consoleAzureChoice, error) {
	var args []string
	switch stage {
	case "azure-subscriptions":
		args = []string{"account", "list", "--query", "[?state=='Enabled'].{name:name,id:id,tenantId:tenantId}", "-o", "json", "--only-show-errors"}
	case "azure-accounts":
		args = []string{"cognitiveservices", "account", "list", "--subscription", subscription, "-o", "json", "--only-show-errors"}
	case "azure-deployments":
		args = []string{"cognitiveservices", "account", "deployment", "list", "--subscription", subscription, "--resource-group", group, "--name", account, "-o", "json", "--only-show-errors"}
	default:
		return nil, fmt.Errorf("unknown Azure discovery stage")
	}
	raw, err := a.liftDiscovery(ctx, "az", args...)
	if err != nil {
		return nil, err
	}
	var choices []consoleAzureChoice
	switch stage {
	case "azure-subscriptions":
		var subs []struct{ Name, ID, TenantID string }
		if err = json.Unmarshal(raw, &subs); err != nil {
			return nil, err
		}
		for _, s := range subs {
			choices = append(choices, consoleAzureChoice{Label: s.Name, ID: s.ID, Tenant: s.TenantID, Detail: "Azure subscription"})
		}
	case "azure-accounts":
		var accounts []foundryAccount
		if err = json.Unmarshal(raw, &accounts); err != nil {
			return nil, err
		}
		for _, a := range accounts {
			if a.Kind != "OpenAI" && a.Kind != "AIServices" {
				continue
			}
			endpoint, err := foundryBaseURL(a)
			if err != nil {
				continue
			}
			choices = append(choices, consoleAzureChoice{Label: a.Name, ID: a.Name, ResourceGroup: a.ResourceGroup, Endpoint: endpoint, Detail: a.ResourceGroup + " · " + a.Location})
		}
	case "azure-deployments":
		var deployments []foundryDeployment
		if err = json.Unmarshal(raw, &deployments); err != nil {
			return nil, err
		}
		for _, d := range deployments {
			if d.Properties.ProvisioningState != "Succeeded" {
				continue
			}
			choices = append(choices, consoleAzureChoice{Label: d.Name, Model: d.Name, Detail: d.Properties.Model.Name + " · " + d.Properties.Model.Version})
		}
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].Label < choices[j].Label })
	return choices, nil
}

func (a *App) consoleLoadInference(ctx context.Context, env agentTUIEnvironment, agent agentTUIAgent) (consoleInferenceSnapshot, error) {
	var snapshot consoleInferenceSnapshot
	server, err := a.consoleCreateTarget(ctx, env)
	if err != nil {
		return snapshot, err
	}
	snapshot.Server = server
	worker := env.app(a)
	raw, err := worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", consoleAgentKind, agent.Name, "-o", "json")
	if err != nil {
		return snapshot, err
	}
	var object struct {
		Metadata struct{ ResourceVersion string }
		Spec     struct {
			Model       map[string]any
			ProviderRef struct{ Name, Namespace string }
		}
	}
	if err = json.Unmarshal(raw, &object); err != nil {
		return snapshot, err
	}
	if object.Metadata.ResourceVersion == "" {
		return snapshot, fmt.Errorf("Agent has no resourceVersion")
	}
	snapshot.Version, snapshot.Model = object.Metadata.ResourceVersion, object.Spec.Model
	raw, err = worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", consoleProviderKind, "-o", "json")
	if err != nil {
		return snapshot, err
	}
	type configuration struct {
		Metadata struct{ Name string }
		Spec     struct {
			Type, Provider, DefaultModel, Model, BaseURL string
			Azure                                        struct{ DeploymentName, APIVersion string }
		}
	}
	var list objectList[configuration]
	if err = decodeConsoleList(raw, &list); err != nil {
		return snapshot, err
	}
	for _, c := range list.Items {
		snapshot.Sources = append(snapshot.Sources, consoleInferenceSource{Kind: "cluster", Namespace: agent.Namespace, Name: c.Metadata.Name, Model: valueOr(c.Spec.DefaultModel, c.Spec.Model), Provider: valueOr(c.Spec.Type, c.Spec.Provider), Endpoint: c.Spec.BaseURL, AzureDeployment: c.Spec.Azure.DeploymentName, AzureAPIVersion: c.Spec.Azure.APIVersion})
	}
	ref := object.Spec.ProviderRef
	if ref.Namespace != "" && ref.Namespace != agent.Namespace {
		raw, err = worker.orkaCapture(ctx, nil, "-n", ref.Namespace, "get", consoleProviderKind, ref.Name, "-o", "json")
		if err != nil {
			return snapshot, fmt.Errorf("cannot read current shared Provider: %w", err)
		}
		var shared configuration
		if err = json.Unmarshal(raw, &shared); err != nil {
			return snapshot, err
		}
		if shared.Metadata.Name != ref.Name {
			return snapshot, fmt.Errorf("shared Provider returned an invalid identity")
		}
		snapshot.Sources = append(snapshot.Sources, consoleInferenceSource{Kind: "cluster", Name: ref.Name, Namespace: ref.Namespace, Model: shared.Spec.DefaultModel, Provider: shared.Spec.Type, Endpoint: shared.Spec.BaseURL, AzureDeployment: shared.Spec.Azure.DeploymentName, AzureAPIVersion: shared.Spec.Azure.APIVersion})
	}
	saved, err := loadConsoleInference(env, agent)
	if err != nil {
		return snapshot, err
	}
	if saved != nil {
		snapshot.Sources = append(snapshot.Sources, *saved)
	}
	return snapshot, nil
}

// The console reads and writes Orka Agents and their Providers only. These are
// the sole kinds any console operation may name.
const (
	consoleAgentKind    = "agents.core.orka.ai"
	consoleProviderKind = "providers.core.orka.ai"
)

const interactiveLiftRetirement = "Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …"

const agentMutationRetirement = "In-place agent tools/inference/prompt editing is retired; edit the authored member/workspace, then test → push → deploy."

func (a *App) consoleSaveInference(ctx context.Context, env agentTUIEnvironment, agent agentTUIAgent, snapshot consoleInferenceSnapshot, source consoleInferenceSource, model string) error {
	return fmt.Errorf("%s", agentMutationRetirement)
}

// Host inference is a local-kind development feature, never a remote runtime.
// Reclassify from kubeconfig, rather than trusting a cosmetic context name.
func (a *App) requireLocalHostInference(ctx context.Context) error {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return err
	}
	posture, err := guard.Classify(kube, a.Cfg.KubeContext)
	if err != nil {
		return err
	}
	if !posture.Local || posture.Host == "" {
		return fmt.Errorf("host Copilot/Azure CLI inference is only available for local kind; remote agents must use cluster-configured inference")
	}
	return nil
}

func consoleOllamaEndpoint(endpoint string, openAI bool) string {
	endpoint = strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/v1")
	if openAI {
		endpoint += "/v1"
	}
	return endpoint
}

func consoleInferencePatch(version, configuration, namespace, model string, currentModel map[string]any) ([]byte, error) {
	if version == "" || configuration == "" {
		return nil, fmt.Errorf("inference update requires an Agent version and configuration")
	}
	updated := map[string]any{}
	for k, v := range currentModel {
		updated[k] = v
	}
	if model != "" {
		updated["name"] = model
	} else {
		delete(updated, "name")
	}
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "add", "path": "/spec/providerRef", "value": map[string]string{"name": configuration, "namespace": namespace}},
		{"op": "add", "path": "/spec/model", "value": updated},
	})
}
