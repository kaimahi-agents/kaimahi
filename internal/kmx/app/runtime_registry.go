package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/kagentcli"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type runtimeRegistration struct {
	adapter agentruntime.Adapter
	run     func(context.Context, agentruntime.Session, string) error
}

func (a *App) registeredChatRuntime(name string) bool {
	for _, registration := range a.chatRuntimes() {
		if string(registration.adapter.ID()) == name {
			return true
		}
	}
	return false
}

// Preference order is explicit, preserving auto's Orka-before-kagent policy.
// Registration owns construction and the temporary legacy presentation driver.
func (a *App) chatRuntimes() []runtimeRegistration {
	return []runtimeRegistration{
		{adapter: orkaRuntimeAdapter{app: a}, run: func(ctx context.Context, session agentruntime.Session, initial string) error {
			s := session.(*orkaRuntimeSession)
			return a.runInteractiveChatBackendInitial(&runtimeChatBackend{session: s, configure: s.backend.Configure}, initial)
		}},
		{adapter: kagentRuntimeAdapter{app: a}, run: func(ctx context.Context, session agentruntime.Session, initial string) error {
			s := session.(*kagentRuntimeSession)
			return a.runKagentSession(ctx, s, initial)
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
		session, err := registration.adapter.Open(ctx, agentruntime.Target{Context: a.Cfg.KubeContext, Namespace: namespace, Name: name, Session: opt.Session})
		if err != nil {
			return err
		}
		defer session.Close()
		return registration.run(ctx, session, opt.Task)
	}
	return fmt.Errorf("unsupported runtime %q", opt.Runtime)
}

// orkaRuntimeAdapter is both the chat Adapter and the lifecycle adapter for
// Orka. create is the command's own flags and is set only by a create: the
// chat registration leaves it nil, and Capabilities declines Render and
// Deploy for an instance that has none.
type orkaRuntimeAdapter struct {
	app    *App
	create *CreateOptions
}

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
	if target.Session != "" {
		return nil, fmt.Errorf("--session is kagent-specific; Orka chat uses fresh Tasks")
	}
	return &orkaRuntimeSession{backend: &orkaChatBackend{app: a.app, agent: target.Name, namespace: target.Namespace}}, nil
}

type kagentRuntimeAdapter struct{ app *App }

func (kagentRuntimeAdapter) ID() agentruntime.ID { return agentruntime.Kagent }
func (a kagentRuntimeAdapter) Probe(ctx context.Context, target agentruntime.Target) (agentruntime.Probe, error) {
	if target.Namespace != "" && target.Namespace != "kagent" {
		return agentruntime.Probe{}, nil
	}
	// Compatibility fallback is resolved by waitServable at Connect. Do not add
	// new discovery permissions or latency to legacy one-shot callers.
	return agentruntime.Probe{Found: true, Agent: agentruntime.AgentRef{Runtime: a.ID(), Context: target.Context, Namespace: "kagent", Kind: "agents.kagent.dev", Name: target.Name}}, nil
}
func (a kagentRuntimeAdapter) Open(ctx context.Context, target agentruntime.Target) (agentruntime.Session, error) {
	if target.Namespace != "" && target.Namespace != "kagent" {
		return nil, fmt.Errorf("kagent chat requires namespace kagent")
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	executable, err := kagentcli.Ensure(kagentcli.Options{Version: a.app.Cfg.KagentVersion, CacheDir: cache, Existing: a.app.Cfg.KagentBin, Log: a.app.Err})
	if err != nil {
		return nil, err
	}
	return &kagentRuntimeSession{app: a.app, executable: executable, name: target.Name, session: target.Session, toolMode: "summary"}, nil
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
