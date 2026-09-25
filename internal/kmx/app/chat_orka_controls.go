package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type configurableChatBackend interface {
	Configure(context.Context, string, *chatRenderer) (bool, error)
}

var orkaAutomaticTools = []string{"recall_memory", "remember", "propose_memory", "search_transcript"}

func (b *orkaChatBackend) enabledToolsSummary(ctx context.Context) (string, error) {
	raw, err := b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "agents.core.orka.ai", b.agent, "-o", "json")
	if err != nil {
		return "", err
	}
	return orkaEnabledToolsSummary(raw)
}

func orkaEnabledToolsSummary(raw []byte) (string, error) {
	var agent struct {
		Spec struct {
			Tools []struct {
				Name    string
				Enabled *bool
			}
		}
	}
	if err := json.Unmarshal(raw, &agent); err != nil {
		return "", err
	}
	enabled := map[string]bool{}
	for _, name := range orkaAutomaticTools {
		enabled[name] = true
	}
	for _, tool := range agent.Spec.Tools {
		if tool.Name != "" && (tool.Enabled == nil || *tool.Enabled) {
			enabled[tool.Name] = true
		}
	}
	names := make([]string, 0, len(enabled))
	for name := range enabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("(%d tools enabled) %s", len(names), strings.Join(names, ", ")), nil
}

// Configure runs only between turns, when no task or input reader owns stdin.
func (b *orkaChatBackend) Configure(ctx context.Context, command string, renderer *chatRenderer) (bool, error) {
	if command == "/inference" || command == "/inference-foundry" || command == "/inference-copilot" {
		if err := b.app.requireLocalHostInference(ctx); err != nil {
			return false, err
		}
	}
	if (command == "/inference" || command == "/inference-foundry") && (!isInteractiveTerminal(b.app.Stdin) || !isInteractiveTerminal(b.app.Out)) {
		return false, fmt.Errorf("%s requires an interactive terminal", command)
	}
	if command == "/inference" {
		m, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "INFERENCE · where the model runs", items: []chatPickerItem{
			{name: "Azure Foundry", detail: "Azure login · host tool loop · no API key"},
			{name: "Copilot CLI", detail: "GitHub login · host tool adapter"},
			{name: "Agent Provider", detail: "Native Orka Task · cluster-configured inference"},
		}, action: "select"})
		if err != nil || !m.accepted {
			return false, err
		}
		return b.Configure(ctx, []string{"/inference-foundry", "/inference-copilot", "/inference-local"}[m.selection], renderer)
	}
	if command == "/inference-foundry" {
		err := b.configureFoundryChat(ctx)
		if err == context.Canceled && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	// Configuration/source changes invalidate reusable transport state.
	b.Close()
	if command == "/lift" {
		if !isInteractiveTerminal(b.app.Stdin) || !isInteractiveTerminal(b.app.Out) {
			return false, fmt.Errorf("/lift requires an interactive terminal")
		}
		before := b.app
		err := b.liftAgent(ctx, renderer)
		return b.app != before, err
	}
	if command == "/inference-copilot" || command == "/inference-local" {
		mode := strings.TrimPrefix(command, "/inference-")
		path := detectCopilotCLI()
		if _, err := quickstartInference(mode, path); err != nil {
			return false, err
		}
		if mode == "copilot" {
			models, err := copilotModels(ctx, path)
			if err != nil {
				return false, err
			}
			items := make([]chatPickerItem, 0, len(models))
			for _, model := range models {
				items = append(items, chatPickerItem{name: model.Model})
			}
			picked, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "COPILOT MODEL", items: items, searchEnabled: true, searchDefault: true})
			if err != nil || !picked.accepted {
				return false, err
			}
			b.app.copilotModel = picked.items[picked.matches()[picked.selection]].name
		}
		b.app.chatInference, b.app.copilotCLI = mode, path
		renderer.statusSection("Inference", b.inferenceLabel())
		return false, nil
	}
	if !isInteractiveTerminal(b.app.Stdin) || !isInteractiveTerminal(b.app.Out) {
		return false, fmt.Errorf("%s requires an interactive terminal", command)
	}
	renderer.finish()
	if command == "/agent" {
		return b.pickAgent(ctx)
	}
	return false, b.pickTools(ctx, renderer)
}

func (b *orkaChatBackend) pickAgent(ctx context.Context) (bool, error) {
	raw, err := b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "agents.core.orka.ai", "-o", "json")
	liveErr := err
	var list struct {
		Items []struct {
			Metadata struct{ Name, Namespace string }
			Spec     struct{ Runtime json.RawMessage }
		}
	}
	if liveErr == nil {
		if err := json.Unmarshal(raw, &list); err != nil {
			return false, err
		}
	}
	var locations []agentLocation
	seen := map[string]bool{}
	for _, agent := range list.Items {
		// This backend executes AI Tasks, not external CLI-runtime agents.
		if len(agent.Spec.Runtime) > 0 && string(agent.Spec.Runtime) != "null" {
			continue
		}
		location := agentLocation{Agent: agent.Metadata.Name, Namespace: agent.Metadata.Namespace, Context: b.app.Cfg.KubeContext, Cluster: b.app.chatClusterName}
		for _, env := range b.app.Run.Env {
			if strings.HasPrefix(env, "KUBECONFIG=") {
				location.Kubeconfig = strings.TrimPrefix(env, "KUBECONFIG=")
			}
		}
		locations = append(locations, location)
		seen[location.key()] = true
	}
	saved, err := loadAgentLocations()
	if err != nil {
		return false, err
	}
	if liveErr != nil && len(saved) == 0 {
		return false, liveErr
	}
	for _, location := range saved {
		if !seen[location.key()] {
			locations = append(locations, location)
			seen[location.key()] = true
		}
	}
	sort.Slice(locations, func(i, j int) bool {
		return locations[i].Agent+locations[i].Context < locations[j].Agent+locations[j].Context
	})
	items := make([]chatPickerItem, 0, len(locations))
	for _, location := range locations {
		detail := location.Namespace + " · " + displayClusterName(location.Context, location.Cluster)
		if location.Agent == b.agent && location.Namespace == b.namespace && location.Context == b.app.Cfg.KubeContext {
			detail += " (current)"
		}
		items = append(items, chatPickerItem{name: location.Agent, detail: detail})
	}
	header := func(width int) string {
		return agentConnectionHeader(b.agent, displayClusterName(b.app.Cfg.KubeContext, b.app.chatClusterName), "Select an agent", width)
	}
	m, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "CONNECT TO AGENT", items: items, searchEnabled: true, searchDefault: true, statusHeader: header})
	if err != nil || !m.accepted {
		return false, err
	}
	selected := locations[m.matches()[m.selection]]
	// Stage the connection on a separate backend: cancellation or lookup failure
	// must leave the current connection intact, even if readiness just completed.
	candidate := &orkaChatBackend{app: b.app, agent: b.agent, namespace: b.namespace}
	header = func(width int) string {
		return agentConnectionHeader(selected.Agent, displayClusterName(selected.Context, selected.Cluster), "Connecting · waiting for Agent Ready", width)
	}
	_, err = runStatusLoading(ctx, b.app.Stdin, b.app.Out, "Checking Agent identity and current-generation readiness", nil, header, func(ctx context.Context) ([]byte, error) { return nil, candidate.connectAgentLocation(ctx, selected) })
	if err != nil {
		return false, err
	}
	b.Close()
	b.app, b.agent, b.namespace = candidate.app, candidate.agent, candidate.namespace
	return true, nil
}

func (b *orkaChatBackend) pickTools(ctx context.Context, renderer *chatRenderer) error {
	renderer.suspendStickyHeader()
	if isInteractiveTerminal(b.app.Out) {
		fmt.Fprint(b.app.Out, "\x1b[H\x1b[2J")
	}
	raw, err := b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "agents.core.orka.ai", b.agent, "-o", "json")
	if err != nil {
		return err
	}
	var agent struct {
		Metadata struct{ ResourceVersion string }
		Spec     struct{ Tools []map[string]any }
	}
	if err := json.Unmarshal(raw, &agent); err != nil {
		return err
	}
	if agent.Metadata.ResourceVersion == "" {
		return fmt.Errorf("Agent has no resourceVersion")
	}
	known := map[string]chatPickerItem{}
	for _, ref := range agent.Spec.Tools {
		name, _ := ref["name"].(string)
		known[name] = chatPickerItem{name: name, enabled: ref["enabled"] != false, detail: "configured"}
	}
	raw, err = b.app.orkaCapture(ctx, nil, "-n", b.namespace, "get", "tools.core.orka.ai", "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct{ Description string }
		}
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	for _, tool := range list.Items {
		item := known[tool.Metadata.Name]
		item.name, item.detail = tool.Metadata.Name, tool.Spec.Description
		known[item.name] = item
	}
	for _, name := range orkaAutomaticTools {
		known[name] = chatPickerItem{name: name, enabled: true, locked: true, detail: "always enabled by Orka v0.1.3"}
	}
	var items []chatPickerItem
	for _, item := range known {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	m, err := runChatPicker(ctx, b.app.Stdin, b.app.Out, chatPicker{title: "TOOLS · " + b.agent, items: items, multiple: true, vim: true, searchEnabled: true, searchDefault: false})
	if err != nil || !m.accepted {
		return err
	}
	patch, err := chatToolSelectionPatch(agent.Metadata.ResourceVersion, agent.Spec.Tools, m.items)
	if err != nil {
		return err
	}
	if patch == nil {
		if summary, err := b.enabledToolsSummary(ctx); err == nil {
			renderer.statusSection("Tools", summary)
		}
		return nil
	}
	renderer.operation("TOOLS", "", colorBlue, "Saving tool selection; waiting for the Agent configuration to become Ready...")
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	raw, err = b.app.orkaCapture(waitCtx, nil, "-n", b.namespace, "patch", "agents.core.orka.ai", b.agent, "--type=json", "-p", string(patch), "-o", "json")
	if err != nil {
		return err
	}
	var updated orkaObject
	if err := json.Unmarshal(raw, &updated); err != nil || updated.Metadata.UID == "" {
		return fmt.Errorf("updated Agent returned no identity")
	}
	if err := b.app.waitOrkaReady(waitCtx, b.namespace, orkaIdentity{Kind: "Agent", Name: b.agent, UID: updated.Metadata.UID, Generation: updated.Metadata.Generation}); err != nil {
		return err
	}
	renderer.operation("TOOLS", "", colorGreen, "Tool selection saved for "+b.agent+". The next message uses the updated tools.")
	if summary, err := orkaEnabledToolsSummary(raw); err == nil {
		renderer.statusSection("Tools", summary)
	}
	return nil
}

func chatToolSelectionPatch(version string, current []map[string]any, items []chatPickerItem) ([]byte, error) {
	selected := map[string]bool{}
	for _, item := range items {
		if !item.locked {
			selected[item.name] = item.enabled
		}
	}
	var refs []map[string]any
	changed := false
	for _, ref := range current {
		copyRef := map[string]any{}
		for k, v := range ref {
			copyRef[k] = v
		}
		name, _ := ref["name"].(string)
		if enabled, ok := selected[name]; ok {
			if enabled != (ref["enabled"] != false) {
				changed = true
				copyRef["enabled"] = enabled
			}
			delete(selected, name)
		}
		refs = append(refs, copyRef)
	}
	for _, item := range items {
		if selected[item.name] {
			refs = append(refs, map[string]any{"name": item.name})
			changed = true
		}
	}
	if !changed {
		return nil, nil
	}
	return json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "add", "path": "/spec/tools", "value": refs},
	})
}
