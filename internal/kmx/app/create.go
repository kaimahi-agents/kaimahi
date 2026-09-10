package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// CreateOptions accepts references, never credential values. Raw numeric flags
// preserve the distinction between an omitted limit and an explicit zero.
type CreateOptions struct {
	Name, Namespace, Description, ProviderType, Model, BaseURL, Secret, SecretKey string
	Instructions, InstructionText, Tools, Skills, Task                            string
	AgentRequestsPerMinute, AgentTokensPerMinute                                  string
	ProviderRequestsPerMinute, ProviderTokensPerMinute                            string
	ResultServiceAccount, OrkaAPIService, ResultPort, SchemaTarget                string
	Out                                                                           string
	NoApply, DryRun                                                               bool
	// Resolved before entering raw terminal mode. Keep the original flags and
	// distinguish an empty file from an instruction source not yet read.
	instructionFileText *string
}

// These kagent readiness/editor helpers retain their existing callers. Orka
// creation has separate schema, dependency and completion checks.
type serverCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	// LastTransitionTime distinguishes a live kagent verdict from one about
	// a credential that has since been replaced (seamverdict.go).
	LastTransitionTime string `json:"lastTransitionTime"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

func (a *App) preflightTools(tools *scaffold.ToolWiring, namespace string) error {
	if tools == nil {
		return nil
	}
	raw, err := a.kubectlCapture("-n", namespace, "get", "remotemcpserver", tools.Server, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read RemoteMCPServer %q in namespace %s: %w", tools.Server, namespace, err)
	}
	var server struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			ObservedGeneration int64             `json:"observedGeneration"`
			Conditions         []serverCondition `json:"conditions"`
			DiscoveredTools    []struct {
				Name string `json:"name"`
			} `json:"discoveredTools"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &server); err != nil {
		return fmt.Errorf("RemoteMCPServer %q returned invalid JSON: %w", tools.Server, err)
	}
	discovered := map[string]bool{}
	for _, tool := range server.Status.DiscoveredTools {
		discovered[tool.Name] = true
	}
	return validateToolServer(tools, server.Metadata.Generation, server.Status.ObservedGeneration, server.Status.Conditions, discovered)
}

func validateToolServer(tools *scaffold.ToolWiring, generation, observed int64, conditions []serverCondition, discovered map[string]bool) error {
	if generation == 0 || observed != generation {
		return fmt.Errorf("RemoteMCPServer %q is still reconciling (generation %d, observed %d)", tools.Server, generation, observed)
	}
	accepted, message := false, ""
	for _, condition := range conditions {
		if condition.Type == "Accepted" && condition.ObservedGeneration == generation {
			accepted, message = condition.Status == "True", condition.Message
		}
	}
	if !accepted {
		detail := ""
		if message != "" {
			detail = ": " + message
		}
		return fmt.Errorf("RemoteMCPServer %q is not Accepted%s", tools.Server, detail)
	}
	var missing []string
	for _, tool := range tools.Tools {
		if !discovered[tool] {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("RemoteMCPServer %q has not discovered allowlisted tool(s): %s\n  Available tools: %s", tools.Server, strings.Join(missing, ", "), strings.Join(sortedBoolKeys(discovered), ", "))
	}
	return nil
}

func sortedBoolKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (a *App) modelConfigExists(name, namespace string) (bool, error) {
	_, err := a.kubectlCapture("-n", namespace, "get", "modelconfig", name, "-o", "name")
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("cannot read modelconfig %q in namespace %s — refusing to guess whether this cluster has a governance plane: %w", name, namespace, err)
	}
}

// A missing kagent ModelConfig is admitted but cannot reconcile. The editor and
// other kagent commands still check this prerequisite explicitly.
func (a *App) preflightModelConfig(name, namespace string) error {
	exists, err := a.modelConfigExists(name, namespace)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	extra := ""
	if name == config.KeylessModelConfig {
		extra = "\n  On a fresh machine that is `kmx up`."
	}
	if name == config.GovernedModelConfig {
		extra = "\n  The governed presets come with the plane: `kmx plane` then `kmx govern`."
	}
	return fmt.Errorf("ModelConfig %q does not exist in namespace %s.\n"+
		"  The API server would accept the Agent and then never reconcile it, silently.\n"+
		"  Existing presets:  kubectl --context %s -n %s get modelconfigs%s", name, namespace, shellArg(a.Cfg.KubeContext), shellArg(namespace), extra)
}
