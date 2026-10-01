package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
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
		items, err := a.retireListDependents(ctx, kind.resource)
		if err != nil {
			return nil, retireInventoryError(kind.label+" Provider references", kind.resource, err)
		}
		for _, item := range items {
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

// Project only reference and identity fields in kubectl, before the result enters
// kmx. In particular, Tasks may contain prompts and tool inputs in spec/status.
// The count makes truncated (including empty) output fail closed.
func retireDependentsProjection() string {
	stringField := func(path string) string {
		return "{{if " + path + "}}{{printf \"%q\" " + path + "}}{{else}}\"\"{{end}}"
	}
	ref := func(path string) string {
		return "{{if " + path + `}}{"name":` + stringField(path+".name") + `,"namespace":` + stringField(path+".namespace") + "}{{else}}null{{end}}"
	}
	var b strings.Builder
	b.WriteString(`{{len .items}}{{"\n"}}{{range .items}}{"metadata":{"name":`)
	b.WriteString(stringField(".metadata.name"))
	b.WriteString(`,"namespace":`)
	b.WriteString(stringField(".metadata.namespace"))
	b.WriteString(`},"spec":{"agentRef":`)
	b.WriteString(ref(".spec.agentRef"))
	b.WriteString(`,"analysisAgentRef":`)
	b.WriteString(ref(".spec.analysisAgentRef"))
	b.WriteString(`,"patchAgentRef":`)
	b.WriteString(ref(".spec.patchAgentRef"))
	b.WriteString(`,"providerRef":`)
	b.WriteString(ref(".spec.providerRef"))
	b.WriteString(`,"ai":{"providerRef":`)
	b.WriteString(ref(".spec.ai.providerRef"))
	b.WriteString(`},"agents":{`)
	for i, role := range []string{"reviewer", "triager", "researcher", "planner", "repairer", "implementer"} {
		if i != 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(role) + ":" + ref(".spec.agents."+role))
	}
	b.WriteString(`},"model":{"fallbacks":[{{range $i, $v := .spec.model.fallbacks}}{{if $i}},{{end}}{"providerRef":`)
	b.WriteString(stringField("$v.providerRef"))
	b.WriteString(`}{{end}}]},"coordination":{"allowedAgents":[{{range $i, $v := .spec.coordination.allowedAgents}}{{if $i}},{{end}}`)
	b.WriteString(ref("$v"))
	b.WriteString(`{{end}}]}},"status":{"phase":`)
	b.WriteString(stringField(".status.phase"))
	b.WriteString(`,"agentExecutionBinding":{"agent":`)
	b.WriteString(ref(".status.agentExecutionBinding.agent"))
	b.WriteString(`}}}{{"\n"}}{{end}}`)
	return b.String()
}

var errRetireListForbidden = errors.New("access forbidden")

func retireInventoryError(label, resource string, err error) error {
	if errors.Is(err, errRetireListForbidden) {
		return fmt.Errorf("cannot inventory %s: cluster-wide list permission on %s required: %w", label, resource, err)
	}
	return fmt.Errorf("cannot inventory %s on %s: %w", label, resource, err)
}

// orkaCapture deliberately collapses all subprocess failures to safe reasons;
// this narrow read boundary additionally separates a real API Forbidden from
// transport, timeout and size failures without returning raw stderr.
func (a *App) retireListDependents(ctx context.Context, resource string) ([]retireDependentItem, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("kubectl request cancelled or timed out")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prepared := a.Command("--request-timeout=10s", "get", resource, "--all-namespaces", "-o=go-template="+retireDependentsProjection())
	cmd := exec.CommandContext(callCtx, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	out := &retireLimitedOutput{orkaBoundedBuffer: orkaBoundedBuffer{remaining: 4 << 20}}
	stderr := &retireLimitedOutput{orkaBoundedBuffer: orkaBoundedBuffer{remaining: 4 << 10}}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	if callCtx.Err() != nil {
		return nil, fmt.Errorf("kubectl request cancelled or timed out")
	}
	if out.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("kubectl response exceeds size limit")
	}
	if err != nil {
		reason := strings.ToLower(stderr.buffer.String())
		switch {
		case strings.Contains(reason, "deadline exceeded") || strings.Contains(reason, "timed out") || strings.Contains(reason, "timeout"):
			return nil, fmt.Errorf("kubectl request timed out")
		case strings.Contains(reason, "(forbidden)") || strings.Contains(reason, "is forbidden:"):
			return nil, errRetireListForbidden
		default:
			return nil, fmt.Errorf("kubectl list request failed")
		}
	}
	lines := strings.Split(strings.TrimSuffix(out.buffer.String(), "\n"), "\n")
	count, parseErr := strconv.Atoi(lines[0])
	if parseErr != nil || count < 0 || len(lines) != count+1 {
		return nil, fmt.Errorf("invalid projected list response")
	}
	items := make([]retireDependentItem, 0, count)
	for _, line := range lines[1:] {
		var item retireDependentItem
		if json.Unmarshal([]byte(line), &item) != nil || item.Metadata.Namespace == "" || item.Metadata.Name == "" || !retireProjectedRefsValid(item) {
			return nil, fmt.Errorf("invalid projected list item")
		}
		items = append(items, item)
	}
	return items, nil
}

func retireProjectedRefsValid(item retireDependentItem) bool {
	refs := []*retireAgentRef{
		item.Spec.AgentRef, item.Spec.AnalysisAgentRef, item.Spec.PatchAgentRef,
		item.Spec.ProviderRef, item.Spec.AI.ProviderRef, item.Status.AgentExecutionBinding.Agent,
	}
	for _, ref := range item.Spec.Agents {
		refs = append(refs, ref)
	}
	for i := range item.Spec.Coordination.AllowedAgents {
		refs = append(refs, &item.Spec.Coordination.AllowedAgents[i])
	}
	for _, ref := range refs {
		if ref != nil && ref.Name == "" {
			return false
		}
	}
	return true
}

type retireLimitedOutput struct {
	orkaBoundedBuffer
	exceeded bool
}

func (b *retireLimitedOutput) Write(p []byte) (int, error) {
	n, err := b.orkaBoundedBuffer.Write(p)
	if err != nil {
		b.exceeded = true
	}
	return n, err
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
		items, err := a.retireListDependents(ctx, kind.resource)
		if err != nil {
			return nil, retireInventoryError(kind.label, kind.resource, err)
		}
		for _, item := range items {
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
