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
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Host sources store routing metadata only; Azure/Copilot retain their own login.
type consoleInferenceSource struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Model         string `json:"model"`
	Endpoint      string `json:"endpoint,omitempty"`
	Tenant        string `json:"tenant,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Secret        string `json:"secret,omitempty"`
	SecretKey     string `json:"secretKey,omitempty"`
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resourceGroup,omitempty"`
	Account       string `json:"account,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
}

type consoleInferenceSnapshot struct {
	Version, Server string
	Model           map[string]any
	Sources         []consoleInferenceSource
}

func consoleInferencePath(env agentTUIEnvironment, agent agentTUIAgent) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(env.Name+"\x00"+agent.key())))
	return filepath.Join(dir, "kmx", "console-inference", id+".json"), nil
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
	if err = source.validate(agent.Runtime); err != nil {
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
	if err = source.validate(agent.Runtime); err != nil {
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

func (s consoleInferenceSource) validate(runtime string) error {
	if err := refuseWizardCredentials(s.Name, s.Model, s.Endpoint, s.Tenant, s.Provider, s.Secret, s.SecretKey); err != nil {
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
		if runtime != "orka" && runtime != "kagent" {
			return fmt.Errorf("unsupported cluster runtime")
		}
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
		if runtime != "orka" {
			return fmt.Errorf("Foundry host inference requires a native Orka agent")
		}
		return (foundryChatConfig{Endpoint: s.Endpoint, Deployment: s.Model, Tenant: s.Tenant}).validate()
	case "copilot":
		if runtime != "orka" {
			return fmt.Errorf("Copilot host inference requires a native Orka agent")
		}
		return nil
	case "ollama", "apikey":
		if runtime != "orka" && runtime != "kagent" {
			return fmt.Errorf("unsupported runtime")
		}
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
			if s.Provider != "openai" && s.Provider != "anthropic" {
				return fmt.Errorf("provider must be openai or anthropic")
			}
			if runtime == "kagent" && s.Provider != "openai" {
				return fmt.Errorf("kagent connector creation currently supports OpenAI-compatible endpoints")
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
	kind, configs := consoleInferenceKinds(agent.Runtime)
	raw, err := worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", kind, agent.Name, "-o", "json")
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
	raw, err = worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", configs, "-o", "json")
	if err != nil {
		return snapshot, err
	}
	type configuration struct {
		Metadata struct{ Name string }
		Spec     struct{ Type, Provider, DefaultModel, Model, BaseURL string }
	}
	var list objectList[configuration]
	if err = decodeConsoleList(raw, &list); err != nil {
		return snapshot, err
	}
	for _, c := range list.Items {
		snapshot.Sources = append(snapshot.Sources, consoleInferenceSource{Kind: "cluster", Namespace: agent.Namespace, Name: c.Metadata.Name, Model: valueOr(c.Spec.DefaultModel, c.Spec.Model), Provider: valueOr(c.Spec.Type, c.Spec.Provider), Endpoint: c.Spec.BaseURL})
	}
	ref := object.Spec.ProviderRef
	if agent.Runtime == "orka" && ref.Namespace != "" && ref.Namespace != agent.Namespace {
		raw, err = worker.orkaCapture(ctx, nil, "-n", ref.Namespace, "get", configs, ref.Name, "-o", "json")
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
		snapshot.Sources = append(snapshot.Sources, consoleInferenceSource{Kind: "cluster", Name: ref.Name, Namespace: ref.Namespace, Model: shared.Spec.DefaultModel, Provider: shared.Spec.Type, Endpoint: shared.Spec.BaseURL})
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

func consoleInferenceKinds(runtime string) (string, string) {
	if runtime == "kagent" {
		return "agents.kagent.dev", "modelconfigs.kagent.dev"
	}
	return "agents.core.orka.ai", "providers.core.orka.ai"
}

func (a *App) consoleSaveInference(ctx context.Context, env agentTUIEnvironment, agent agentTUIAgent, snapshot consoleInferenceSnapshot, source consoleInferenceSource, model string) error {
	if agent.External {
		return fmt.Errorf("inference editing requires an AI agent")
	}
	if err := refuseWizardCredentials(model); err != nil {
		return err
	}
	if source.Kind != "cluster" {
		if err := source.validate(agent.Runtime); err != nil {
			return err
		}
	}
	if source.Kind == "foundry" || source.Kind == "copilot" {
		if !env.Local {
			return fmt.Errorf("remote environments cannot use local Copilot or Azure CLI inference; configure a cluster Provider instead")
		}
		if err := env.app(a).requireLocalHostInference(ctx); err != nil {
			return err
		}
		if source.Kind == "foundry" {
			client, err := newFoundryChatClient(foundryChatConfig{Endpoint: source.Endpoint, Deployment: source.Model, Tenant: source.Tenant})
			if err != nil {
				return err
			}
			defer client.http.CloseIdleConnections()
			if _, err = client.bearer(ctx); err != nil {
				return err
			}
		} else {
			path := detectCopilotCLI()
			if path == "" {
				return fmt.Errorf("install Copilot CLI, then sign in with copilot login")
			}
			if copilotLoginStatus(ctx, path) != "logged in" {
				return fmt.Errorf("Copilot is not signed in; run copilot login, then retry")
			}
			models, err := copilotModels(ctx, path)
			if err != nil {
				return err
			}
			found := false
			for _, m := range models {
				if m.Model == source.Model {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("model is not available through the signed-in Copilot account")
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return saveConsoleInference(env, agent, &source)
	}
	server, err := a.consoleCreateTarget(ctx, env)
	if err != nil {
		return err
	}
	if server != snapshot.Server {
		return fmt.Errorf("environment changed; reopen inference to review the target")
	}
	worker := env.app(a)
	kind, _ := consoleInferenceKinds(agent.Runtime)
	currentRaw, err := worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", kind, agent.Name, "-o", "json")
	if err != nil {
		return err
	}
	var current struct {
		Metadata struct{ ResourceVersion string }
	}
	if err = json.Unmarshal(currentRaw, &current); err != nil {
		return err
	}
	if snapshot.Version == "" || current.Metadata.ResourceVersion != snapshot.Version {
		return fmt.Errorf("agent changed; reopen inference before saving")
	}
	if source.Kind != "cluster" {
		if source.Kind == "foundry-cluster" {
			var err error
			source, err = worker.consolePrepareClusterFoundry(ctx, agent, source)
			if err != nil {
				return err
			}
		}
		if err := worker.consoleCreateConnector(ctx, agent, source); err != nil {
			return err
		}
		model = "" // New connector's default model is the chosen model.
	}
	patch, err := consoleInferencePatch(agent.Runtime, snapshot.Version, source.Name, valueOr(source.Namespace, agent.Namespace), model, snapshot.Model)
	if err != nil {
		return err
	}
	raw, err := worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "patch", kind, agent.Name, "--type=json", "-p", string(patch), "-o", "json")
	if err != nil {
		return fmt.Errorf("could not update agent; any new connector remains available: %w", err)
	}
	if err = saveConsoleInference(env, agent, nil); err != nil {
		return fmt.Errorf("agent updated, but clearing host override failed: %w", err)
	}
	if agent.Runtime == "orka" {
		var updated orkaObject
		if err = json.Unmarshal(raw, &updated); err != nil {
			return err
		}
		if updated.Metadata.UID == "" {
			return fmt.Errorf("updated Agent returned no identity")
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return worker.waitOrkaReady(waitCtx, agent.Namespace, orkaIdentity{Kind: "Agent", Name: agent.Name, UID: updated.Metadata.UID, Generation: updated.Metadata.Generation})
	}
	return nil
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

func (a *App) consolePrepareClusterFoundry(ctx context.Context, agent agentTUIAgent, s consoleInferenceSource) (consoleInferenceSource, error) {
	if err := s.validate(agent.Runtime); err != nil {
		return s, err
	}
	if s.Account != "" {
		target := chatLiftTarget{Subscription: s.Subscription}
		account := foundryAccount{Name: s.Account, ResourceGroup: s.ResourceGroup}
		args := append([]string{"cognitiveservices", "account", "show"}, foundryScope(target, account)...)
		raw, err := a.liftDiscovery(ctx, "az", append(args, "-o", "json", "--only-show-errors")...)
		if err != nil {
			return s, err
		}
		if err = json.Unmarshal(raw, &account); err != nil {
			return s, err
		}
		if account.Properties.DisableLocalAuth {
			return s, fmt.Errorf("Foundry disables API keys; this runtime needs worker-side workload identity support before keyless remote setup is possible")
		}
		endpoint, err := foundryBaseURL(account)
		if err != nil {
			return s, err
		}
		if strings.TrimRight(endpoint, "/") != strings.TrimRight(s.Endpoint, "/") {
			return s, fmt.Errorf("Foundry endpoint changed; reopen setup")
		}
		args = append([]string{"cognitiveservices", "account", "keys", "list"}, foundryScope(target, account)...)
		raw, err = a.liftDiscovery(ctx, "az", append(args, "-o", "json", "--only-show-errors")...)
		if err != nil {
			return s, err
		}
		var keys struct{ Key1 string }
		if json.Unmarshal(raw, &keys) != nil || keys.Key1 == "" {
			return s, fmt.Errorf("Foundry returned no usable API key")
		}
		suffix, err := randomHex(4)
		if err != nil {
			return s, err
		}
		s.Secret, s.SecretKey = "kmx-foundry-"+suffix, "api-key"
		body, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": s.Secret, "namespace": agent.Namespace}, "type": "Opaque", "stringData": map[string]string{"api-key": keys.Key1}})
		if err != nil {
			return s, err
		}
		if _, err = a.orkaCapture(ctx, body, "-n", agent.Namespace, "create", "-f", "-", "-o", "name"); err != nil {
			return s, fmt.Errorf("could not create cluster Foundry credential Secret: %w", err)
		}
	}
	// Test from the target cluster using the mounted Secret, not host credentials.
	s.Endpoint = strings.TrimRight(s.Endpoint, "/")
	if !strings.HasSuffix(s.Endpoint, "/openai/v1") {
		s.Endpoint += "/openai/v1"
	}
	if err := a.verifyFoundryEndpointKey(ctx, agent.Namespace, s.Secret, s.SecretKey, s.Endpoint, s.Model); err != nil {
		return s, fmt.Errorf("cluster Foundry verification failed; Secret %s retained: %w", s.Secret, err)
	}
	s.Kind, s.Provider = "apikey", "openai"
	s.Endpoint = strings.TrimRight(s.Endpoint, "/")
	if !strings.HasSuffix(s.Endpoint, "/openai/v1") {
		s.Endpoint += "/openai/v1"
	}
	return s, nil
}

func (a *App) consoleCreateConnector(ctx context.Context, agent agentTUIAgent, s consoleInferenceSource) error {
	if err := s.validate(agent.Runtime); err != nil {
		return err
	}
	if agent.Runtime == "kagent" {
		spec := map[string]any{"provider": "OpenAI", "model": s.Model, "apiKeySecret": s.Secret, "apiKeySecretKey": s.SecretKey, "openAI": map[string]any{"baseUrl": s.Endpoint}}
		if s.Kind == "ollama" {
			spec = map[string]any{"provider": "Ollama", "model": s.Model, "ollama": map[string]any{"host": consoleOllamaEndpoint(s.Endpoint, false)}}
		}
		body, _ := json.Marshal(map[string]any{"apiVersion": "kagent.dev/v1alpha2", "kind": "ModelConfig", "metadata": map[string]string{"name": s.Name, "namespace": agent.Namespace}, "spec": spec})
		_, err := a.orkaCapture(ctx, body, "-n", agent.Namespace, "create", "--validate=strict", "-f", "-", "-o", "json")
		return err
	}
	secret, key, provider, endpoint := s.Secret, s.SecretKey, s.Provider, s.Endpoint
	if s.Kind == "ollama" {
		secret = s.Name + "-keyless"
		key = "api-key"
		provider = "openai"
		endpoint = consoleOllamaEndpoint(endpoint, true)
	}
	bundle, err := createOrkaBundle(CreateOptions{Name: s.Name, Namespace: agent.Namespace, ProviderType: provider, Model: s.Model, BaseURL: endpoint, Secret: secret, SecretKey: key})
	if err != nil {
		return err
	}
	if err = a.orkaAbsent(ctx, agent.Namespace, bundle.Provider); err != nil {
		return err
	}
	if s.Kind == "ollama" {
		// A separate, create-only dummy Secret satisfies the Orka Provider schema;
		// it contains no credential and never overwrites a user's Secret.
		body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": secret, "namespace": agent.Namespace}, "stringData": map[string]string{"api-key": "not-used-by-this-endpoint"}})
		if _, err = a.orkaCapture(ctx, body, "-n", agent.Namespace, "create", "-f", "-", "-o", "name"); err != nil {
			return fmt.Errorf("keyless connector Secret could not be created: %w", err)
		}
	}
	id, err := a.createOrkaObject(ctx, agent.Namespace, bundle.Provider)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return a.waitOrkaReady(waitCtx, agent.Namespace, id)
}

func consoleOllamaEndpoint(endpoint string, openAI bool) string {
	endpoint = strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/v1")
	if openAI {
		endpoint += "/v1"
	}
	return endpoint
}

func consoleInferencePatch(runtime, version, configuration, namespace, model string, currentModel map[string]any) ([]byte, error) {
	if version == "" || configuration == "" {
		return nil, fmt.Errorf("inference update requires an Agent version and configuration")
	}
	patch := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": version}}
	switch runtime {
	case "orka":
		updated := map[string]any{}
		for k, v := range currentModel {
			updated[k] = v
		}
		if model != "" {
			updated["name"] = model
		} else {
			delete(updated, "name")
		}
		patch = append(patch, map[string]any{"op": "add", "path": "/spec/providerRef", "value": map[string]string{"name": configuration, "namespace": namespace}}, map[string]any{"op": "add", "path": "/spec/model", "value": updated})
	case "kagent":
		patch = append(patch, map[string]any{"op": "add", "path": "/spec/declarative/modelConfig", "value": configuration})
	default:
		return nil, fmt.Errorf("unsupported inference runtime %q", runtime)
	}
	return json.Marshal(patch)
}
