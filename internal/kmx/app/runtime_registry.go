package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type runtimeRegistration struct {
	adapter agentruntime.Adapter
	run     func(context.Context, agentruntime.Session, string) error
}

// Registration owns construction. The list is ordered, and stays a list
// rather than a single entry because auto-detection is an ordered walk: the
// seam exists so a second platform can be added without the caller learning
// about it.
func (a *App) chatRuntimes() []runtimeRegistration {
	return []runtimeRegistration{
		{adapter: orkaRuntimeAdapter{app: a}, run: func(ctx context.Context, session agentruntime.Session, initial string) error {
			s := session.(*orkaRuntimeSession)
			return a.runInteractiveChatBackendInitial(&runtimeChatBackend{session: s, configure: s.backend.Configure}, initial)
		}},
	}
}

func (a *App) openRuntimeChat(opt ChatOptions, name, namespace string) error {
	ctx, cancel := context.WithCancel(a.operationContext())
	defer cancel()
	for _, registration := range a.chatRuntimes() {
		if string(registration.adapter.ID()) != opt.Runtime {
			continue
		}
		session, err := registration.adapter.Open(ctx, agentruntime.Target{Context: a.Cfg.KubeContext, Namespace: namespace, Name: name})
		if err != nil {
			return err
		}
		defer session.Close()
		return registration.run(ctx, session, opt.Task)
	}
	return fmt.Errorf("unsupported runtime %q", opt.Runtime)
}

type orkaRuntimeAdapter struct{ app *App }

func (orkaRuntimeAdapter) ID() agentruntime.ID { return agentruntime.Orka }
func (a orkaRuntimeAdapter) Probe(ctx context.Context, target agentruntime.Target) (agentruntime.Probe, error) {
	namespace := target.Namespace
	if namespace == "" {
		namespace = OrkaNamespace
	}
	raw, err := a.app.orkaCapture(ctx, nil, "api-resources", "--api-group=core.orka.ai", "-o", "name")
	if err != nil {
		return agentruntime.Probe{}, fmt.Errorf("cannot discover chat runtimes: %w; select --runtime explicitly", err)
	}
	hasOrka := false
	for _, resource := range strings.Fields(string(raw)) {
		if resource == "agents.core.orka.ai" {
			hasOrka = true
		}
	}
	if !hasOrka {
		return agentruntime.Probe{}, nil
	}
	raw, err = a.app.orkaCapture(ctx, nil, "-n", namespace, "get", "agents.core.orka.ai", target.Name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return agentruntime.Probe{}, fmt.Errorf("cannot inspect Orka Agent %s/%s: %w", namespace, target.Name, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return agentruntime.Probe{}, nil
	}
	var agent struct {
		Metadata struct{ Name, Namespace, UID string }
		Spec     struct{ Runtime json.RawMessage }
	}
	if json.Unmarshal(raw, &agent) != nil || agent.Metadata.Name != target.Name || agent.Metadata.Namespace != namespace {
		return agentruntime.Probe{}, fmt.Errorf("invalid Orka Agent identity")
	}
	if len(agent.Spec.Runtime) > 0 && string(agent.Spec.Runtime) != "null" {
		return agentruntime.Probe{}, fmt.Errorf("Agent %s uses an external CLI runtime; this chat supports Orka AI agents", target.Name)
	}
	return agentruntime.Probe{Found: true, Agent: agentruntime.AgentRef{Runtime: a.ID(), Context: target.Context, Namespace: namespace, Kind: "agents.core.orka.ai", Name: target.Name, UID: agent.Metadata.UID}}, nil
}
func (a orkaRuntimeAdapter) Open(ctx context.Context, target agentruntime.Target) (agentruntime.Session, error) {
	return &orkaRuntimeSession{backend: &orkaChatBackend{app: a.app, agent: target.Name, namespace: target.Namespace}}, nil
}

func resolveRegisteredRuntime(ctx context.Context, registrations []runtimeRegistration, target agentruntime.Target) (agentruntime.AgentRef, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, registration := range registrations {
		probe, err := registration.adapter.Probe(ctx, target)
		if err != nil {
			return agentruntime.AgentRef{}, err
		}
		if probe.Found {
			return probe.Agent, nil
		}
	}
	return agentruntime.AgentRef{}, fmt.Errorf("Agent %s/%s was not found in the registered runtimes", target.Namespace, target.Name)
}
