package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

const (
	orkaUpProvider      = "local"
	orkaHelloToolsAgent = "hello-tools"

	orkaHelloWorldInstructions = `You are Kaimahi's hello-world agent, running on Kubernetes via Orka.
Greet the user warmly and answer briefly in plain text. If asked who you are,
say you are a declarative Orka agent. Always answer directly yourself and
never ask the user questions.`

	orkaHelloToolsInstructions = `You are Kaimahi's read-only Kubernetes helper, running on Kubernetes via Orka.
For questions about live Kubernetes resources, call k8s-get-resources and
answer only from its output. Never invent resource names. Copy resource names
exactly. The tool can list resources but cannot change the cluster.`
)

func (a *App) upOrka() error {
	if err := a.runPhase(phase{current: 1, total: 6, name: upPhaseName("cluster")}, a.stepCluster); err != nil {
		return err
	}
	a.verifySelectedLocalModel()
	if a.selectedLocalModel == nil {
		if err := a.runPhase(phase{current: 2, total: 6, name: upPhaseName("ollama")}, a.stepOllama); err != nil {
			return err
		}
		if err := a.runPhase(phase{current: 3, total: 6, name: upPhaseName("model")}, a.stepModel); err != nil {
			return err
		}
	} else {
		a.notef("SKIP   Reusing %s/%s; no in-cluster Ollama or model pull",
			a.selectedLocalModel.Provider, a.selectedLocalModel.Model)
	}
	if err := a.runPhase(phase{current: 4, total: 6, name: upPhaseName("orka")}, a.stepOrka); err != nil {
		return err
	}
	return a.runPhase(phase{current: 5, total: 6, name: "Deploy Orka agents in parallel"}, func() error {
		a.notef("Two lanes are running; every output line is tagged.")
		return a.runLanes([]lane{
			{"agent", func(b *App) error { return b.stepOrkaAgent() }},
			{"tools-agent", func(b *App) error { return b.stepOrkaToolsAgent() }},
		})
	})
}

func (a *App) stepOrka() error {
	opt := OrkaOptions{
		Provider: orkaUpProvider,
		Model:    a.Cfg.Model,
		ModelURL: "http://ollama.ollama.svc.cluster.local:11434/v1",
	}
	if a.selectedLocalModel == nil {
		if err := a.OrkaInstall(opt); err != nil {
			return err
		}
		return a.quickstartResultReader()
	}

	opt.Model = a.selectedLocalModel.Model
	opt.ModelURL = strings.TrimSuffix(a.selectedLocalModel.Endpoint, "/") + "/v1"
	if err := a.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		return err
	}
	if err := a.applyOrkaProvider(opt); err != nil {
		return err
	}
	return a.quickstartResultReader()
}

func (a *App) stepOrkaAgent() error {
	return a.applyOrkaUpAgent(config.DefaultAgent, "Minimal hello-world agent", orkaHelloWorldInstructions, nil)
}

func (a *App) stepOrkaToolsAgent() error {
	if err := a.installQuickstartK8sTool(); err != nil {
		return err
	}
	return a.applyOrkaUpAgent(orkaHelloToolsAgent, "Read-only Kubernetes helper",
		orkaHelloToolsInstructions, []string{quickstartK8sTool})
}

func (a *App) applyOrkaUpAgent(name, description, instructions string, tools []string) error {
	spec := map[string]any{
		"providerRef":  map[string]any{"name": orkaUpProvider, "namespace": OrkaNamespace},
		"systemPrompt": map[string]any{"inline": instructions},
	}
	if len(tools) > 0 {
		refs := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			refs = append(refs, map[string]any{"name": tool})
		}
		spec["tools"] = refs
	}
	agent := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1",
		"kind":       "Agent",
		"metadata": map[string]any{
			"name":      name,
			"namespace": OrkaNamespace,
			"labels":    map[string]string{"app.kubernetes.io/managed-by": "kmx"},
			"annotations": map[string]string{
				"kaimahi.dev/description": description,
			},
		},
		"spec": spec,
	}
	body, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(a.operationContext(), 5*time.Minute)
	defer cancel()
	if _, err := a.orkaCapture(ctx, body, "-n", OrkaNamespace, "apply",
		"--dry-run=server", "--validate=strict", "-f", "-", "-o", "json"); err != nil {
		return fmt.Errorf("Agent/%s strict server apply preflight failed: %w", name, err)
	}
	raw, err := a.orkaCapture(ctx, body, "-n", OrkaNamespace, "apply",
		"--server-side", "--field-manager=kmx-up", "--validate=strict", "-f", "-", "-o", "json")
	if err != nil {
		return fmt.Errorf("apply Agent/%s: %w", name, err)
	}
	var object orkaObject
	if err := json.Unmarshal(raw, &object); err != nil ||
		object.Metadata.UID == "" || object.Metadata.Generation < 1 {
		return fmt.Errorf("applied Agent/%s returned no valid identity", name)
	}
	a.notef("Applied Agent/%s (UID %s); waiting for current-generation Ready.",
		name, object.Metadata.UID)
	return a.waitOrkaReady(ctx, OrkaNamespace, orkaIdentity{
		Kind: "Agent", Name: name, UID: object.Metadata.UID, Generation: object.Metadata.Generation,
	})
}
