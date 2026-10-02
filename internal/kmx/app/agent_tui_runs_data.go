package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
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
	if scaffold.ValidateNamespace(agent.Namespace) != nil {
		return result, fmt.Errorf("cannot list Tasks: invalid namespace")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	worker := env.app(a).withRunContext(ctx)
	const pageLimit, scanLimit = 2, 200
	const partial = "Some Tasks could not be scanned; recent runs may be missing."
	seen := make(map[string]bool)
	cursor := ""
	scanned := 0
	for {
		path := fmt.Sprintf("/apis/core.orka.ai/v1alpha1/namespaces/%s/tasks?limit=%d", agent.Namespace, pageLimit)
		if cursor != "" {
			path += "&continue=" + url.QueryEscape(cursor)
		}
		raw, err := worker.orkaCapture(ctx, nil, "get", "--raw", path)
		if err != nil {
			if scanned == 0 && len(seen) == 0 {
				return result, fmt.Errorf("cannot list Tasks: %w", err)
			}
			result.Missing = partial
			break
		}
		var envelope struct {
			Metadata json.RawMessage `json:"metadata"`
			Items    json.RawMessage `json:"items"`
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(raw, &envelope) != nil || !strings.HasPrefix(strings.TrimSpace(string(envelope.Items)), "[") ||
			json.Unmarshal(raw, &list) != nil || len(list.Items) > pageLimit {
			if scanned == 0 && len(seen) == 0 {
				return result, fmt.Errorf("cannot read Task list")
			}
			result.Missing = partial
			break
		}
		// Kubernetes omits continue at the end of a list. A present value
		// must be a string, never null, an object, or unbounded command input.
		var metadata map[string]json.RawMessage
		if len(envelope.Metadata) != 0 && (json.Unmarshal(envelope.Metadata, &metadata) != nil || metadata == nil) {
			if scanned == 0 && len(seen) == 0 {
				return result, fmt.Errorf("cannot read Task list")
			}
			result.Missing = partial
			break
		}
		next := ""
		if value, ok := metadata["continue"]; ok {
			if len(value) > 2048 || json.Unmarshal(value, &next) != nil || strings.TrimSpace(string(value)) == "null" {
				if scanned == 0 && len(seen) == 0 {
					return result, fmt.Errorf("cannot read Task list")
				}
				result.Missing = partial
				break
			}
		}
		if scanned+len(list.Items) > scanLimit {
			result.Missing = partial
			break
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
			phase := task.Status.Phase
			switch phase {
			case "", "Pending", "Scheduled", "Running", "Finalizing", "Succeeded", "Failed", "Cancelled":
			default:
				phase = ""
				result.Missing = "Some Tasks have invalid identity or data."
			}
			result.Roots = append(result.Roots, consoleRunRef{Name: task.Metadata.Name, Namespace: task.Metadata.Namespace, UID: task.Metadata.UID, Status: phase, CreatedAt: task.Metadata.CreationTimestamp})
		}
		scanned += len(list.Items)
		if next == "" {
			break
		}
		if scanned >= scanLimit || seen[next] || next == cursor || ctx.Err() != nil {
			result.Missing = partial
			break
		}
		seen[next] = true
		cursor = next
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
