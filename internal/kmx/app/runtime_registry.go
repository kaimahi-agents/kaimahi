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

type orkaRuntimeAdapter struct {
	app *App
	// create carries the Orka create flags the closed portable document
	// deliberately does not model (description, first Task prompt, offline
	// schema target) plus this command's deployment/output flags. DESIGN.md
	// §1 keeps app-specific CLI shapes in app: they stay here, in the
	// app-owned adapter, rather than leaking Orka flags into the neutral
	// runtime package's RenderOptions/DeployOptions. Chat registration leaves
	// it zero; only the lifecycle verbs read it (runtime_orka.go).
	create CreateOptions
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

// Capabilities is Task 4's skeleton declaration for legacy kagent: every
// lifecycle flag is false until Task 8 wraps its existing combined-status
// slice behind Status (DESIGN.md §3 declares Render/Deploy/Evaluate
// permanently unsupported for this runtime; only Status is ever expected to
// flip true).
func (kagentRuntimeAdapter) Capabilities() agentruntime.Capabilities {
	return agentruntime.Capabilities{}
}

func (a kagentRuntimeAdapter) Render(context.Context, agentruntime.PortableAgent, agentruntime.RenderOptions) (agentruntime.RenderedBundle, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Render, agentruntime.VerbRender); err != nil {
		return agentruntime.RenderedBundle{}, err
	}
	return agentruntime.RenderedBundle{}, fmt.Errorf("kagent render: not yet implemented")
}

func (a kagentRuntimeAdapter) Deploy(context.Context, agentruntime.RenderedBundle, agentruntime.DeployOptions) (agentruntime.AgentRef, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Deploy, agentruntime.VerbDeploy); err != nil {
		return agentruntime.AgentRef{}, err
	}
	return agentruntime.AgentRef{}, fmt.Errorf("kagent deploy: not yet implemented")
}

func (a kagentRuntimeAdapter) Status(context.Context, agentruntime.AgentRef, agentruntime.StatusOptions) (agentruntime.LifecycleStatus, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Status, agentruntime.VerbStatus); err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	return agentruntime.LifecycleStatus{}, fmt.Errorf("kagent status: not yet implemented")
}

func (a kagentRuntimeAdapter) Evaluate(context.Context, agentruntime.AgentRef, agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Evaluate, agentruntime.VerbEvaluate); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	return agentruntime.EvaluationReceipt{}, fmt.Errorf("kagent evaluate: not yet implemented")
}

// resolveRegisteredRuntime keeps PR #197's exact call shape (a slice of
// runtimeRegistration, since chat registrations also carry a run function
// Adapter alone does not model) but delegates the actual ordered-probe,
// no-fallback-on-error bookkeeping to the shared, neutral
// agentruntime.Registry — the same logic Task 4 moved into internal/kmx/runtime
// so it is no longer duplicated between chat and future lifecycle dispatch.
func resolveRegisteredRuntime(ctx context.Context, registrations []runtimeRegistration, target agentruntime.Target) (agentruntime.AgentRef, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	adapters := make([]agentruntime.Adapter, len(registrations))
	for i, registration := range registrations {
		adapters[i] = registration.adapter
	}
	registry, err := agentruntime.NewRegistry(adapters...)
	if err != nil {
		return agentruntime.AgentRef{}, err
	}
	return registry.Resolve(ctx, target)
}

// lifecycleVerbError is the one place both existing adapters' skeleton
// LifecycleAdapter methods decide whether to run or refuse: a verb a
// runtime's Capabilities declares unsupported returns the shared
// UnsupportedVerbError instead of being attempted. Task 4 registers only
// skeleton Capabilities (every lifecycle flag false) for the existing
// Orka/legacy-kagent adapters; later tasks flip a flag to true and replace
// that verb's method body with real behavior, without changing this
// dispatch rule.
func lifecycleVerbError(id agentruntime.ID, supported bool, verb string) error {
	if supported {
		return nil
	}
	return &agentruntime.UnsupportedVerbError{Runtime: id, Verb: verb}
}

// detectOrkaPlatform is the Kubernetes-aware half of DESIGN.md §1's shared
// platform auto-detection: it inspects installed API resources once for
// Orka's core.orka.ai Agent CRD and returns a neutral installed/error result
// for agentruntime.SelectPlatform. A read error is reported, never silently
// treated as absence.
func (a *App) detectOrkaPlatform(ctx context.Context) agentruntime.PlatformDetection {
	raw, err := a.orkaCapture(ctx, nil, "api-resources", "--api-group=core.orka.ai", "-o", "name")
	if err != nil {
		return agentruntime.PlatformDetection{Err: fmt.Errorf("cannot detect Orka installation: %w", err)}
	}
	for _, resource := range strings.Fields(string(raw)) {
		if resource == "agents.core.orka.ai" {
			return agentruntime.PlatformDetection{Installed: true}
		}
	}
	return agentruntime.PlatformDetection{}
}

// kagentV1APIGroupVersion and kagentV1AgentTemplateKind pin the exact
// prerequisite DESIGN.md §1 names: kagent-v1 is identified by the
// kagent.dev/v1alpha3 AgentTemplate CRD, not merely by anything named
// "agenttemplates" under the kagent.dev group at whatever version happens
// to be preferred.
const (
	kagentV1APIGroupVersion   = "kagent.dev/v1alpha3"
	kagentV1AgentTemplateKind = "AgentTemplate"
)

// kagentV1APIResourceList is the shape of Kubernetes' own per-group-version
// discovery document (GET /apis/<group>/<version>), which lists exactly the
// resources served under that one version — the only discovery shape that
// can distinguish "AgentTemplate exists under v1alpha3" from "AgentTemplate
// exists under some other version" or "nothing under v1alpha3 at all".
type kagentV1APIResourceList struct {
	Resources []struct {
		Kind string `json:"kind"`
	} `json:"resources"`
}

// detectKagentV1Platform is the Kubernetes-aware half of DESIGN.md §1's
// shared platform auto-detection for kagent v1. Legacy kagent shares the
// same kagent.dev API group, so a group-level check (e.g. `api-resources
// --api-group=kagent.dev`) cannot tell kagent-v1 apart from it: both would
// list a kagent.dev resource regardless of which version actually serves
// it. Querying the versioned discovery document directly — `get --raw
// /apis/kagent.dev/v1alpha3`, Kubernetes' own APIResourceList for exactly
// that group-version — proves the exact served version DESIGN.md §1
// requires: the apiserver answers NotFound unless some CRD actually serves
// v1alpha3, and lists only that version's resources when it does.
func (a *App) detectKagentV1Platform(ctx context.Context) agentruntime.PlatformDetection {
	if err := ctx.Err(); err != nil {
		return agentruntime.PlatformDetection{Err: err}
	}
	raw, err := a.kubectlCapture("get", "--raw", "/apis/"+kagentV1APIGroupVersion)
	if err != nil {
		if isNotFound(err) {
			// kagent.dev/v1alpha3 is simply not a registered group-version:
			// either kagent.dev is entirely absent, or only some other
			// version (e.g. legacy kagent's v1alpha2) is served. Both mean
			// kagent-v1 is not installed, never a read failure.
			return agentruntime.PlatformDetection{}
		}
		return agentruntime.PlatformDetection{Err: fmt.Errorf("cannot detect kagent v1 installation: %w", err)}
	}
	var resources kagentV1APIResourceList
	if err := json.Unmarshal([]byte(raw), &resources); err != nil {
		return agentruntime.PlatformDetection{Err: fmt.Errorf("cannot parse %s API discovery: %w", kagentV1APIGroupVersion, err)}
	}
	for _, resource := range resources.Resources {
		if resource.Kind == kagentV1AgentTemplateKind {
			return agentruntime.PlatformDetection{Installed: true}
		}
	}
	return agentruntime.PlatformDetection{}
}
