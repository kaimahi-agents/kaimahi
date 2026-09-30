package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// These are the agent-reference surfaces in Orka v0.2.0. An execution binding
// is embedded in Task status, not a separate Kubernetes resource.
type retireAgentRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type retireDependentItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		AgentRef         *retireAgentRef            `json:"agentRef"`
		AnalysisAgentRef *retireAgentRef            `json:"analysisAgentRef"`
		PatchAgentRef    *retireAgentRef            `json:"patchAgentRef"`
		Agents           map[string]*retireAgentRef `json:"agents"`
		ProviderRef      *retireAgentRef            `json:"providerRef"`
		Model            struct {
			Fallbacks []struct {
				ProviderRef string `json:"providerRef"`
			} `json:"fallbacks"`
		} `json:"model"`
		AI struct {
			ProviderRef *retireAgentRef `json:"providerRef"`
		} `json:"ai"`
		Coordination struct {
			AllowedAgents []retireAgentRef `json:"allowedAgents"`
		} `json:"coordination"`
	} `json:"spec"`
	Status struct {
		Phase                 string `json:"phase"`
		AgentExecutionBinding struct {
			Agent *retireAgentRef `json:"agent"`
		} `json:"agentExecutionBinding"`
	} `json:"status"`
}

func (a *App) retireProviderDependents(ctx context.Context, namespace, name string) ([]string, error) {
	var found []string
	for _, kind := range []struct{ resource, label string }{{"tasks.core.orka.ai", "Task"}, {"agents.core.orka.ai", "Agent"}} {
		raw, err := a.orkaCapture(ctx, nil, "get", kind.resource, "--all-namespaces", "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("cannot inventory %s Provider references: cluster-wide list permission on %s required: %w", kind.label, kind.resource, err)
		}
		var list struct {
			Items []retireDependentItem `json:"items"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Items == nil {
			return nil, fmt.Errorf("cannot inventory %s Provider references: invalid list response", kind.label)
		}
		for _, item := range list.Items {
			owner := item.Metadata.Namespace
			if owner == "" || item.Metadata.Name == "" {
				return nil, fmt.Errorf("cannot inventory %s Provider references: item missing name or namespace", kind.label)
			}
			id := kind.label + " " + owner + "/" + item.Metadata.Name
			if kind.label == "Agent" {
				if owner == namespace && item.Metadata.Name == name {
					continue
				}
				if retireRefMatches(item.Spec.ProviderRef, owner, namespace, name) {
					found = append(found, id+" (spec.providerRef)")
				}
				for _, fallback := range item.Spec.Model.Fallbacks {
					if owner == namespace && fallback.ProviderRef == name {
						found = append(found, id+" (spec.model.fallbacks.providerRef)")
						break
					}
				}
			} else if item.Status.Phase != "Succeeded" && item.Status.Phase != "Failed" && item.Status.Phase != "Cancelled" && retireRefMatches(item.Spec.AI.ProviderRef, owner, namespace, name) {
				phase := item.Status.Phase
				if phase == "" {
					phase = "phase not yet reported"
				}
				found = append(found, id+" (spec.ai.providerRef; "+phase+")")
			}
		}
	}
	sort.Strings(found)
	return found, nil
}

func retireRefMatches(ref *retireAgentRef, ownerNamespace, namespace, name string) bool {
	if ref == nil || ref.Name != name {
		return false
	}
	targetNamespace := ref.Namespace
	if targetNamespace == "" {
		targetNamespace = ownerNamespace
	}
	return targetNamespace == namespace
}

// retireAgentDependents inventories every namespaced Orka v0.2.0 reference
// surface before a caller may retire an Agent. All list failures are fatal:
// a partial inventory must never authorize deletion.
func (a *App) retireAgentDependents(ctx context.Context, namespace, name string) ([]string, error) {
	kinds := []struct{ resource, label string }{
		{"tasks.core.orka.ai", "Task"},
		{"gatewaybindings.gateway.orka.ai", "GatewayBinding"},
		{"repositoryscans.core.orka.ai", "RepositoryScan"},
		{"repositorymonitors.core.orka.ai", "RepositoryMonitor"},
		{"agents.core.orka.ai", "Agent"},
	}
	var found []string
	for _, kind := range kinds {
		raw, err := a.orkaCapture(ctx, nil, "get", kind.resource, "--all-namespaces", "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("cannot inventory %s: cluster-wide list permission on %s required: %w", kind.label, kind.resource, err)
		}
		var list struct {
			Items []retireDependentItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil || list.Items == nil {
			return nil, fmt.Errorf("cannot inventory %s: invalid %s list response", kind.label, kind.resource)
		}
		for _, item := range list.Items {
			owner := item.Metadata.Namespace
			if owner == "" || item.Metadata.Name == "" {
				return nil, fmt.Errorf("cannot inventory %s: list item missing name or namespace", kind.label)
			}
			identifier := kind.label + " " + owner + "/" + item.Metadata.Name
			switch kind.label {
			case "Task":
				phase := item.Status.Phase
				if phase != "Succeeded" && phase != "Failed" && phase != "Cancelled" {
					if retireRefMatches(item.Spec.AgentRef, owner, namespace, name) || retireRefMatches(item.Status.AgentExecutionBinding.Agent, owner, namespace, name) {
						if phase == "" {
							phase = "phase not yet reported"
						}
						found = append(found, identifier+" ("+phase+")")
					}
				}
			case "GatewayBinding":
				if retireRefMatches(item.Spec.AgentRef, owner, namespace, name) {
					found = append(found, identifier+" (spec.agentRef)")
				}
			case "RepositoryScan":
				if retireRefMatches(item.Spec.AnalysisAgentRef, owner, namespace, name) {
					found = append(found, identifier+" (spec.analysisAgentRef)")
				}
				if retireRefMatches(item.Spec.PatchAgentRef, owner, namespace, name) {
					found = append(found, identifier+" (spec.patchAgentRef)")
				}
			case "RepositoryMonitor":
				for _, role := range []string{"reviewer", "triager", "researcher", "planner", "repairer", "implementer"} {
					if retireRefMatches(item.Spec.Agents[role], owner, namespace, name) {
						found = append(found, identifier+" (spec.agents."+role+")")
					}
				}
			case "Agent":
				if owner == namespace && item.Metadata.Name == name {
					continue
				}
				for i := range item.Spec.Coordination.AllowedAgents {
					if retireRefMatches(&item.Spec.Coordination.AllowedAgents[i], owner, namespace, name) {
						found = append(found, identifier+" (coordination.allowedAgents)")
						break
					}
				}
			}
		}
	}
	sort.Strings(found)
	return found, nil
}
