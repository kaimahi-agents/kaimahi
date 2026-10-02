package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview/orka"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// consoleRunRef contains only the identity and display fields needed to open
// a run; the Task's prompt and other payload are deliberately not retained.
type consoleRunRef struct {
	Name, UID, Namespace, Status string
	CreatedAt                    time.Time
}

type consoleRunList struct {
	Roots   []consoleRunRef
	Count   int
	Missing string
}

// consoleRecentOrkaRuns reads the selected Agent's namespace in its selected
// Kubernetes context. An unreadable list is an error; an invalid member is
// partial evidence, never evidence that there are no runs.
func (a *App) consoleRecentOrkaRuns(ctx context.Context, env agentTUIEnvironment, agent agentTUIAgent) (consoleRunList, error) {
	var result consoleRunList
	worker := env.app(a).withRunContext(ctx)
	raw, err := worker.orkaCapture(ctx, nil, "-n", agent.Namespace, "get", orkaPlural("Task"), "-o", "json")
	if err != nil {
		return result, fmt.Errorf("cannot list Tasks: %w", err)
	}
	var list objectList[json.RawMessage]
	if err := decodeConsoleList(raw, &list); err != nil {
		return result, fmt.Errorf("cannot read Task list")
	}
	for _, item := range list.Items {
		var task struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name, Namespace, UID string
				CreationTimestamp    time.Time
				Labels               map[string]string
				Annotations          map[string]string
				OwnerReferences      []struct{ Kind, Name, UID string }
			}
			Spec struct {
				AgentRef struct{ Name, Namespace string }
			}
			Status struct{ Phase string }
		}
		if err := json.Unmarshal(item, &task); err != nil || task.Kind != "" && task.Kind != "Task" ||
			scaffold.ValidateObjectName(task.Metadata.Name) != nil || task.Metadata.Namespace == "" || task.Metadata.UID == "" || task.Spec.AgentRef.Name == "" {
			result.Missing = "Some Tasks have invalid identity or data."
			continue
		}
		if task.Metadata.Namespace != agent.Namespace || task.Spec.AgentRef.Name != agent.Name {
			continue
		}
		refNS := task.Spec.AgentRef.Namespace
		if refNS == "" {
			refNS = task.Metadata.Namespace
		}
		if refNS != agent.Namespace {
			continue
		}
		labels := task.Metadata.Labels
		child := false
		if labels["orka.ai/coordinator"] == "true" && labels["orka.ai/delegated-agent"] == task.Spec.AgentRef.Name && labels["orka.ai/parent-task"] != "" {
			for _, owner := range task.Metadata.OwnerReferences {
				if owner.Kind == "Task" && owner.Name != "" && owner.UID != "" && labels["orka.ai/parent-task"] == orka.ParentSelector(owner.Name) && task.Metadata.Annotations["orka.ai/parent-task-name"] == owner.Name {
					child = true
					break
				}
			}
		}
		if child {
			continue
		}
		result.Roots = append(result.Roots, consoleRunRef{Name: task.Metadata.Name, Namespace: task.Metadata.Namespace, UID: task.Metadata.UID, Status: task.Status.Phase, CreatedAt: task.Metadata.CreationTimestamp})
	}
	sort.SliceStable(result.Roots, func(i, j int) bool {
		if result.Roots[i].CreatedAt.Equal(result.Roots[j].CreatedAt) {
			return result.Roots[i].UID < result.Roots[j].UID
		}
		return result.Roots[i].CreatedAt.After(result.Roots[j].CreatedAt)
	})
	result.Count = len(result.Roots)
	if result.Count > 20 {
		result.Roots = result.Roots[:20]
	}
	return result, nil
}
