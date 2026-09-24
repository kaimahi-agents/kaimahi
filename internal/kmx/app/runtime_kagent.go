package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Legacy kagent lifecycle: DESIGN.md §3 wraps this runtime's existing fixed
// install, list and combined-status mechanics behind the shared seam without
// changing any of them. Only Status is ever supported here — Render, Deploy
// and Evaluate are permanently unsupported, because legacy kagent is
// installed from fixed manifests and implies no portable conversion — and
// PR #197's Adapter/Session below continue to own chat unchanged.

// kagentStatusSnapshot is one combined kagent read: the verbatim kubectl
// objects plus the same list demultiplexed by kind. It exists so the runtime
// slice and the app-owned aggregate sections `kmx status` prints around it
// are the same snapshot — a consumer can never find an Agent in `items`
// that the counts beside them never saw.
type kagentStatusSnapshot struct {
	items  []json.RawMessage
	agents objectList[agentStatus]
	models objectList[modelStatus]
	pods   objectList[podStatus]
}

// readKagentStatusSnapshot performs exactly the one combined get `kmx
// status` has always made and demultiplexes it by kind. It is the only read
// of these objects in a status run.
func (a *App) readKagentStatusSnapshot() (*kagentStatusSnapshot, error) {
	raw, err := a.kubectlCapture("-n", config_kagentNamespace, "get",
		"agents.kagent.dev,modelconfigs,pods", "-o", "json", statusRequestTimeout)
	if err != nil {
		return nil, err
	}
	var combined struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &combined); err != nil {
		return nil, err
	}
	snapshot := &kagentStatusSnapshot{items: combined.Items}
	if snapshot.items == nil {
		// An empty cluster publishes `[]`, not `null`: a consumer iterates
		// items, and null makes them vanish with a zero exit code.
		snapshot.items = []json.RawMessage{}
	}
	for _, item := range snapshot.items {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item, &kind); err != nil {
			return nil, err
		}
		switch kind.Kind {
		case "Agent":
			var agent agentStatus
			if err := json.Unmarshal(item, &agent); err != nil {
				return nil, err
			}
			snapshot.agents.Items = append(snapshot.agents.Items, agent)
		case "ModelConfig":
			var model modelStatus
			if err := json.Unmarshal(item, &model); err != nil {
				return nil, err
			}
			snapshot.models.Items = append(snapshot.models.Items, model)
		case "Pod":
			var pod podStatus
			if err := json.Unmarshal(item, &pod); err != nil {
				return nil, err
			}
			snapshot.pods.Items = append(snapshot.pods.Items, pod)
		}
	}
	return snapshot, nil
}

// Capabilities declares exactly what this runtime can be asked to do.
// DESIGN.md §3 is explicit: Render, Deploy and Evaluate are permanently
// unsupported for legacy kagent, and only Status — the retained combined
// runtime slice below — is supported. Session-level flags remain Session's
// own concern (kagentRuntimeSession.Capabilities), not this static
// declaration.
func (kagentRuntimeAdapter) Capabilities() agentruntime.Capabilities {
	return agentruntime.Capabilities{Status: true}
}

func (a kagentRuntimeAdapter) Render(context.Context, agentruntime.PortableAgent, agentruntime.RenderOptions) (agentruntime.RenderedBundle, error) {
	return agentruntime.RenderedBundle{}, lifecycleVerbError(a.ID(), a.Capabilities().Render, agentruntime.VerbRender)
}

func (a kagentRuntimeAdapter) Deploy(context.Context, agentruntime.RenderedBundle, agentruntime.DeployOptions) (agentruntime.AgentRef, error) {
	return agentruntime.AgentRef{}, lifecycleVerbError(a.ID(), a.Capabilities().Deploy, agentruntime.VerbDeploy)
}

func (a kagentRuntimeAdapter) Evaluate(context.Context, agentruntime.AgentRef, agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	return agentruntime.EvaluationReceipt{}, lifecycleVerbError(a.ID(), a.Capabilities().Evaluate, agentruntime.VerbEvaluate)
}

// Status wraps the legacy runtime slice `kmx status` has always printed: the
// kagent pod readiness and restart line, derived from the one combined read
// above. Legacy kagent has no template/instance split, so PairStatus.Fields
// carries that slice directly and Instance stays nil; there is never a
// merged readiness boolean.
//
// It deliberately reports nothing else. The governance, Ollama, MCP and
// certificate sections beside it are app-owned aggregation (status.go) and
// never come from a LifecycleAdapter — this call only hands that aggregation
// the same snapshot it read, through the caller's sink.
func (a kagentRuntimeAdapter) Status(ctx context.Context, ref agentruntime.AgentRef, _ agentruntime.StatusOptions) (agentruntime.LifecycleStatus, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Status, agentruntime.VerbStatus); err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	// Legacy kagent is fixed to its own namespace, so a different one is a
	// conflict rather than something to silently read past.
	if namespace := strings.TrimSpace(ref.Namespace); namespace != "" && namespace != config_kagentNamespace {
		return agentruntime.LifecycleStatus{}, fmt.Errorf("the legacy kagent runtime reports namespace %s; it cannot report %s", config_kagentNamespace, namespace)
	}
	if err := ctx.Err(); err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	snapshot, err := a.app.readKagentStatusSnapshot()
	if err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	if a.snapshot != nil {
		*a.snapshot = *snapshot
	}
	ready, restarts, _ := podSummary(snapshot.pods.Items)
	return agentruntime.LifecycleStatus{Pair: agentruntime.PairStatus{Fields: []agentruntime.Field{{
		Label: string(a.ID()),
		Value: fmt.Sprintf("%d/%d pods ready, %d restarts", ready, len(snapshot.pods.Items), restarts),
	}}}}, nil
}

// Kagent retains its native session IDs, history and HITL continuation protocol.
// The terminal coordinator provides a decision callback; model/session work is
// behind the same Session contract as Orka. No Orka resource is involved.
type kagentRuntimeSession struct {
	app                                                *App
	executable, name, session, base, cliBase, toolMode string
	posture                                            *chatGovernancePosture
	stop, closeCLIBase                                 func()
	decide                                             func(context.Context, *streamView, *chatRenderer) (*streamView, error)
	// Compatibility driver renders the native stream directly. Other consumers
	// use typed events through runtimeEventRenderer instead.
	renderer        *chatRenderer
	approvalFailure bool
	decisionFailure bool
}

func (s *kagentRuntimeSession) Agent() agentruntime.AgentRef {
	return agentruntime.AgentRef{Runtime: agentruntime.Kagent, Context: s.app.Cfg.KubeContext, Namespace: "kagent", Kind: "agents.kagent.dev", Name: s.name}
}
func (s *kagentRuntimeSession) Capabilities() agentruntime.Capabilities {
	return agentruntime.Capabilities{Streaming: true, Resume: true, Approvals: true}
}
func (s *kagentRuntimeSession) Commands() []agentruntime.Command {
	var commands []agentruntime.Command
	for _, command := range slashCommandList {
		if !containsChatCommand(commonChatCommands(), command.name) {
			commands = append(commands, agentruntime.Command{Name: command.name, Usage: command.usage})
		}
	}
	return commands
}
func (s *kagentRuntimeSession) output(emit agentruntime.Emit, verbose bool) *chatRenderer {
	if s.renderer != nil {
		return s.renderer
	}
	return runtimeEventRenderer(emit, verbose)
}
func (s *kagentRuntimeSession) Connect(ctx context.Context, emit agentruntime.Emit) (agentruntime.Status, error) {
	status := agentruntime.Status{Agent: s.Agent()}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	if len(s.name) > 63 || !agentNameRE.MatchString(s.name) {
		return status, fmt.Errorf("agent name %q is not a valid Kubernetes name", s.name)
	}
	if err := s.app.waitServable(s.name); err != nil {
		return status, err
	}
	port, stop, err := s.app.portForward()
	if err != nil {
		return status, err
	}
	s.stop = stop
	s.base = "http://127.0.0.1:" + port
	// The pinned CLI is pointed at the compatibility hop instead: it sends an
	// empty messageId, which the agent refuses. s.base stays the forward, so
	// kmx's own session, history, task and HITL calls are unchanged.
	if s.cliBase, s.closeCLIBase, err = s.app.legacyChatEndpoint(s.base); err != nil {
		s.Close()
		return status, err
	}
	r := s.output(emit, s.app.chatVerbose)
	s.posture, err = s.app.refreshChatPosture(s.name, r)
	if err != nil {
		s.Close()
		return status, err
	}
	if s.session != "" {
		if err = s.app.showSessionHistory(s.base, s.session, s.name, s.toolMode, r); err != nil {
			s.Close()
			return status, err
		}
	}
	return status, nil
}
func (s *kagentRuntimeSession) Send(ctx context.Context, turn agentruntime.Turn, emit agentruntime.Emit) error {
	s.approvalFailure, s.decisionFailure = false, false
	r := s.output(emit, turn.Verbose)
	r.beginAssistant(s.name)
	if s.posture != nil && s.posture.modelGoverned {
		r.assistantOperation(s.name, "KAIMAHI ROUTE", "", colorYellow, "Seam: model proxy\nConfiguration: verified through ready plane at chat start\nPer-call decision: not exposed by kagent stream")
	}
	view, err := s.app.invokeStream(ctx, s.executable, s.cliBase, s.name, turn.Message, s.session, s.toolMode, r, s.posture)
	if view != nil && view.context != "" {
		s.session = view.context
	}
	if err != nil {
		return err
	}
	for view.approval != nil {
		s.approvalFailure = true
		if view.approvalErr != nil {
			return view.approvalErr
		}
		if s.decide == nil {
			return fmt.Errorf("kagent requires an approval/input handler; no decision submitted")
		}
		view, err = s.decide(ctx, view, r)
		if view != nil && view.context != "" {
			s.session = view.context
		}
		if err != nil {
			return err
		}
	}
	s.approvalFailure = false
	return nil
}
func (s *kagentRuntimeSession) Close() {
	if s.closeCLIBase != nil {
		s.closeCLIBase()
		s.closeCLIBase, s.cliBase = nil, ""
	}
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
}
