package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

// AgentTUIOptions selects the two environments shown by kmx console.
type AgentTUIOptions struct {
	LocalContext, RemoteContext, Namespace string
	Demo                                   bool
}

type agentTUIEnvironment struct {
	Name, Kubeconfig string
	Local            bool
}

func (e agentTUIEnvironment) app(a *App) *App {
	return appAtAgentLocation(a, agentLocation{Context: e.Name, Kubeconfig: e.Kubeconfig})
}

// Only prefer identities that still exist in the correct column. Explicit flags
// are handled by the model and never fall back to another context on failure.
func agentTUIPreferredEnvironments(envs []agentTUIEnvironment, prefs map[string]string, configured string) [2]string {
	var selected [2]string
	for column, candidates := range [][]string{
		{prefs["tui/local"], configured},
		{prefs["tui/remote"], prefs["context"], configured},
	} {
		for _, name := range candidates {
			for _, env := range envs {
				if name != "" && env.Name == name && env.Local == (column == 0) {
					selected[column] = name
					break
				}
			}
			if selected[column] != "" {
				break
			}
		}
	}
	return selected
}

type agentTUIAgent struct {
	Name, Namespace, Runtime, Version, Ready  string
	Provider, Model, Endpoint, InferenceReady string
	InferenceConfig                           string
	External                                  bool
	SystemPrompt, PromptSource, PromptError   string
	Tools                                     []agentTUITool
}

type agentTUITool struct {
	Name, Kind, Detail string
	Disabled           bool
}

type agentTUISystemPrompt struct {
	Inline       string
	ConfigMapRef *struct{ Name, Key string }
}

func (a agentTUIAgent) toolSummary() string {
	enabled := 0
	var names []string
	for _, tool := range a.Tools {
		name := tool.Name
		if tool.Disabled {
			name += " (off)"
		} else {
			enabled++
		}
		names = append(names, name)
	}
	if len(a.Tools) == 0 {
		return "Tools: 0"
	}
	return "Tools: " + fmt.Sprintf("%d/%d enabled", enabled, len(a.Tools)) + " · " + strings.Join(names, ", ")
}

func (a *App) agentTUIPrompt(ctx context.Context, namespace string, prompt agentTUISystemPrompt, row *agentTUIAgent) {
	row.SystemPrompt, row.PromptSource = prompt.Inline, "inline"
	if prompt.ConfigMapRef == nil {
		return
	}
	ref := prompt.ConfigMapRef
	row.PromptSource = "ConfigMap " + namespace + "/" + ref.Name + " · key " + ref.Key
	row.SystemPrompt = ""
	raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "configmap", ref.Name, "-o", "json")
	var cm struct{ Data map[string]string }
	if err == nil {
		err = json.Unmarshal(raw, &cm)
	}
	if err != nil {
		row.PromptError = "System prompt unavailable: " + err.Error()
		return
	}
	value, ok := cm.Data[ref.Key]
	if !ok {
		row.PromptError = "System prompt unavailable: ConfigMap key not found"
		return
	}
	row.SystemPrompt = value
}

func (a agentTUIAgent) key() string   { return a.Runtime + "/" + a.Namespace + "/" + a.Name }
func (a agentTUIAgent) canChat() bool { return !a.External }
func (a agentTUIAgent) canLift() bool { return !a.External }

type agentTUIColumn struct {
	Env       agentTUIEnvironment
	Agents    []agentTUIAgent
	Selection int
	Loading   bool
	Error     string
}

type agentTUIMetadata struct {
	Name, Namespace string
	Generation      int64
	Labels          map[string]string
}

func (m agentTUIMetadata) version() string {
	if v := m.Labels["app.kubernetes.io/version"]; v != "" {
		return v
	}
	if m.Generation > 0 {
		return fmt.Sprintf("unversioned · gen %d", m.Generation)
	}
	return "unknown"
}

// Discovery uses the same context classification as the mutation guard. Saved
// agent locations retain their private kubeconfig rather than changing defaults.
func (a *App) agentTUIEnvironments(ctx context.Context) ([]agentTUIEnvironment, error) {
	raw, err := a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
	if err != nil {
		return nil, err
	}
	kube, err := guard.ParseKubeconfig(raw)
	if err != nil {
		return nil, err
	}
	var envs []agentTUIEnvironment
	defaultKubeconfig := ""
	for _, entry := range a.Command("config", "view").Env {
		if strings.HasPrefix(entry, "KUBECONFIG=") {
			defaultKubeconfig = strings.TrimPrefix(entry, "KUBECONFIG=")
		}
	}
	if defaultKubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		defaultKubeconfig = filepath.Join(home, ".kube", "config")
	}
	seen := map[string]int{}
	for _, c := range kube.Contexts {
		p, err := guard.Classify(kube, c.Name)
		if err != nil || p.Host == "" {
			continue
		}
		seen[c.Name] = len(envs)
		envs = append(envs, agentTUIEnvironment{Name: c.Name, Kubeconfig: defaultKubeconfig, Local: p.Local})
	}
	locations, err := loadAgentLocations()
	if err != nil {
		return envs, err
	}
	saved := map[string]bool{}
	for _, l := range locations {
		if saved[l.Context] || l.Kubeconfig == "" {
			continue
		}
		raw, err := appAtAgentLocation(a, l).orkaCapture(ctx, nil, "config", "view", "-o", "json")
		if err != nil {
			continue
		}
		kube, err := guard.ParseKubeconfig(raw)
		if err != nil {
			continue
		}
		p, err := guard.Classify(kube, l.Context)
		if err != nil || p.Host == "" {
			continue
		}
		env := agentTUIEnvironment{Name: l.Context, Kubeconfig: l.Kubeconfig, Local: p.Local}
		if index, ok := seen[l.Context]; ok {
			envs[index] = env
		} else {
			seen[l.Context] = len(envs)
			envs = append(envs, env)
		}
		saved[l.Context] = true
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	return envs, nil
}

// Empty inventories are valid only when Kubernetes explicitly returns an array.
// Missing/null items (including Status objects) are not evidence of absence.
func decodeConsoleList(raw []byte, dst any) error {
	var envelope struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	items := strings.TrimSpace(string(envelope.Items))
	if !strings.HasPrefix(items, "[") {
		return fmt.Errorf("invalid Kubernetes list: items must be an array")
	}
	return json.Unmarshal(raw, dst)
}

// Inventory reads are bounded and independent per column. Missing API groups
// are empty inventories; permission/transport errors remain visible as errors.
//
// The console reads Orka Agents and nothing else. The retired runtime's kinds
// are not listed even when a cluster still serves them: every operation this
// dashboard offers — chat, create, inference, tools, lift — was removed for
// that runtime, so listing its Agents would advertise actions that cannot run.
// What an older installation left on a cluster is outside this command.
func (a *App) agentTUIInventory(ctx context.Context, env agentTUIEnvironment, namespace string) ([]agentTUIAgent, error) {
	a = env.app(a)
	raw, err := a.orkaCapture(ctx, nil, "api-resources", "-o", "name")
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, kind := range strings.Fields(string(raw)) {
		kinds[kind] = true
	}
	var agents []agentTUIAgent
	var problems []string
	read := func(kind, ns string, dst any) bool {
		raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", kind, "-o", "json")
		if err == nil {
			err = decodeConsoleList(raw, dst)
		}
		if err != nil {
			problems = append(problems, kind+": "+err.Error())
			return false
		}
		return true
	}
	if kinds["agents.core.orka.ai"] {
		var list objectList[struct {
			Metadata agentTUIMetadata
			Spec     struct {
				ProviderRef  struct{ Name, Namespace string }
				Model        struct{ Name string }
				Runtime      json.RawMessage
				SystemPrompt agentTUISystemPrompt
				Tools        []struct {
					Name    string
					Enabled *bool
				}
			}
			Status struct{ Ready *bool }
		}]
		if read("agents.core.orka.ai", namespace, &list) {
			var providers objectList[struct {
				Metadata agentTUIMetadata
				Spec     struct{ Type, BaseURL, DefaultModel string }
				Status   struct{ Ready *bool }
			}]
			if len(list.Items) > 0 {
				read("providers.core.orka.ai", namespace, &providers)
			}
			var tools objectList[struct {
				Metadata agentTUIMetadata
				Spec     struct {
					Description string
					HTTP        *struct{ URL, Method string }
					MCP         json.RawMessage
				}
			}]
			toolReadError := ""
			for _, item := range list.Items {
				if len(item.Spec.Tools) == 0 {
					continue
				}
				raw, err := a.orkaCapture(ctx, nil, "-n", namespace, "get", "tools.core.orka.ai", "-o", "json")
				if err == nil {
					err = decodeConsoleList(raw, &tools)
				}
				if err != nil {
					toolReadError = "definition unavailable: " + err.Error()
				}
				break
			}
			for _, item := range list.Items {
				row := agentTUIAgent{Name: item.Metadata.Name, Namespace: namespace, Runtime: "orka", Version: item.Metadata.version(), Ready: agentTUIReady(item.Status.Ready), Provider: item.Spec.ProviderRef.Name, Model: item.Spec.Model.Name, InferenceReady: "unknown"}
				row.External = len(item.Spec.Runtime) > 0 && string(item.Spec.Runtime) != "null"
				a.agentTUIPrompt(ctx, namespace, item.Spec.SystemPrompt, &row)
				for _, ref := range item.Spec.Tools {
					tool := agentTUITool{Name: ref.Name, Kind: "Orka Tool", Disabled: ref.Enabled != nil && !*ref.Enabled, Detail: valueOr(toolReadError, "definition not found")}
					for _, def := range tools.Items {
						if def.Metadata.Name != ref.Name {
							continue
						}
						tool.Detail = def.Spec.Description
						if def.Spec.HTTP != nil {
							tool.Kind = "HTTP " + valueOr(def.Spec.HTTP.Method, "POST")
							tool.Detail += " · " + agentTUIEndpoint(def.Spec.HTTP.URL)
						}
						if len(def.Spec.MCP) > 0 && string(def.Spec.MCP) != "null" {
							tool.Kind = "MCP"
						}
						break
					}
					row.Tools = append(row.Tools, tool)
				}
				if refNS := item.Spec.ProviderRef.Namespace; refNS != "" && refNS != namespace {
					var provider struct {
						Spec   struct{ Type, BaseURL, DefaultModel string }
						Status struct{ Ready *bool }
					}
					raw, err := a.orkaCapture(ctx, nil, "-n", refNS, "get", "providers.core.orka.ai", row.Provider, "-o", "json")
					if err == nil {
						err = json.Unmarshal(raw, &provider)
					}
					if err != nil {
						problems = append(problems, "Provider "+refNS+"/"+row.Provider+": "+err.Error())
					} else {
						row.Model = valueOr(row.Model, provider.Spec.DefaultModel)
						row.Endpoint = agentTUIEndpoint(provider.Spec.BaseURL)
						row.InferenceReady = agentTUIReady(provider.Status.Ready)
					}
					row.Provider = refNS + "/" + row.Provider
					agents = append(agents, row)
					continue
				}
				for _, p := range providers.Items {
					if p.Metadata.Name != row.Provider {
						continue
					}
					row.Provider += " (" + p.Spec.Type + ")"
					row.Model = valueOr(row.Model, p.Spec.DefaultModel)
					row.Endpoint = agentTUIEndpoint(p.Spec.BaseURL)
					row.InferenceReady = agentTUIReady(p.Status.Ready)
					break
				}
				agents = append(agents, row)
			}
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Name == agents[j].Name {
			return agents[i].key() < agents[j].key()
		}
		return agents[i].Name < agents[j].Name
	})
	for i := range agents {
		source, err := loadConsoleInference(env, agents[i])
		if err != nil {
			problems = append(problems, "host inference configuration: "+err.Error())
			continue
		}
		if source != nil {
			agents[i].Provider = "host " + source.Kind
			agents[i].Model = source.Model
			agents[i].Endpoint = agentTUIEndpoint(source.Endpoint)
			if source.Kind == "copilot" {
				agents[i].Endpoint = "Copilot CLI on this host"
			}
			agents[i].InferenceReady = "not checked (host chat override)"
			agents[i].InferenceConfig = "Saved for console chat; cluster Provider unchanged"
		}
	}
	if len(problems) > 0 {
		return agents, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return agents, nil
}

func agentTUIReady(ready *bool) string {
	if ready == nil {
		return "unknown"
	}
	return boolState(*ready)
}

func agentTUIEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Scheme + "://" + u.Host // Credentials, query and path are not inventory fields.
}

func agentTUIDemoColumns() [2]agentTUIColumn {
	return [2]agentTUIColumn{
		{Env: agentTUIEnvironment{Name: "kind-local-demo", Local: true}, Agents: []agentTUIAgent{
			{Name: "assistant", Namespace: OrkaNamespace, Runtime: "orka", Version: "v1.2.0", Ready: "yes", Provider: "local-model (openai)", Model: "qwen3:8b", Endpoint: "http://localhost:11434", InferenceReady: "yes",
				SystemPrompt: "You are a helpful cluster assistant.\nUse available tools to inspect resources before answering.\nExplain what you observed and distinguish facts from assumptions.", PromptSource: "inline",
				Tools: []agentTUITool{{Name: "k8s-get-resources", Kind: "HTTP POST", Detail: "List Kubernetes resources"}, {Name: "web-fetch", Kind: "HTTP POST", Detail: "Retrieve a web page", Disabled: true}}},
			{Name: "researcher", Namespace: OrkaNamespace, Runtime: "orka", Version: "unversioned · gen 3", Ready: "no", Provider: "foundry (openai)", Model: "gpt-4.1", InferenceReady: "unknown"},
			{Name: "reporter", Namespace: OrkaNamespace, Runtime: "orka", Version: "unversioned · gen 1", Ready: "yes", Provider: "local-model (openai)", Model: "qwen3:8b", InferenceReady: "yes", External: true, SystemPrompt: "Answer briefly and plainly.", PromptSource: "inline"},
		}},
		{Env: agentTUIEnvironment{Name: "remote-demo"}, Agents: []agentTUIAgent{
			{Name: "assistant", Namespace: OrkaNamespace, Runtime: "orka", Version: "v1.1.0", Ready: "yes", Provider: "hosted (openai)", Model: "gpt-4.1", Endpoint: "https://inference.example.com", InferenceReady: "yes"},
		}},
	}
}
