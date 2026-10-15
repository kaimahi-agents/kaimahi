package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

// ChatOptions selects the Orka Agent a chat session is opened against.
type ChatOptions struct {
	Agent     string
	Task      string
	Verbose   bool
	Runtime   string
	Namespace string
}

// ChatWithOptions opens the interactive Orka session. Task, when set, is
// sent as the first turn.
//
// Unguarded, like every other read-shaped command here. Calling it
// "read-only" would be wrong — it spends budget and writes a ledger row. It
// is unguarded because the line being drawn is not "mutates" but "can be
// aimed somewhere unintended": chat runs through kubectl carrying an explicit
// --context, so it lands wherever the rest of the invocation was already
// going to land. Prompting on the most-used command would buy nothing and
// teach people to type past confirmations.
func (a *App) ChatWithOptions(opt ChatOptions) error {
	a.chatVerbose = opt.Verbose
	if err := checkChatRuntime(opt.Runtime); err != nil {
		return err
	}
	agent := opt.Agent
	// Orka chat is always a session: a Task is created, polled for its own
	// result over a single connection, and never resubmitted. An initial
	// message is the first turn of that session, not a one-shot; the one-shot
	// Task is kmx run.
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	runtime, namespace, err := a.resolveInteractiveChat(opt, agent)
	if err != nil {
		return err
	}
	opt.Runtime = runtime
	return a.openRuntimeChat(opt, agent, namespace)
}

// checkChatRuntime answers an explicit --runtime before anything reaches a
// cluster.
//
// The retired runtime is refused BY NAME rather than resolved to Orka. A
// caller who named it asked for a different platform; answering from Orka
// instead would answer a question nobody put, against an agent that is not
// the one they meant. That one word is why it appears below at all.
func checkChatRuntime(name string) error {
	switch name {
	case "", "auto", "orka":
		return nil
	case "kagent":
		return fmt.Errorf("--runtime kagent is unsupported for chat.\n" +
			"  Run kmx targets to see supported runtime operations; kmx chat is Orka-only.")
	default:
		return fmt.Errorf("unknown chat runtime %q; use auto or orka", name)
	}
}

type orkaChatBackend struct {
	liftHeader       *liftHeader
	app              *App
	agent, namespace string
	chatContext      context.Context
	toolConnections  map[string]*copilotToolConnection
	resultSession    *orkaResultSession
}

func (b *orkaChatBackend) Agent() string { return b.agent }

func quickstartResultOptions(namespace, task string) CreateOptions {
	return CreateOptions{Namespace: namespace, ResultServiceAccount: orkaResultAccount, Task: task}
}

func (b *orkaChatBackend) Connect(ctx context.Context, renderer *chatRenderer) ([]cliui.Field, error) {
	b.chatContext = ctx
	location, err := b.app.chatLocation(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := b.enabledToolsSummary(ctx)
	if err != nil {
		return nil, err
	}
	renderer.statusStart(b.agent, b.app.Cfg.KubeContext)
	runtime, tasks := "Orka Task worker Jobs", "Each message creates one fresh local Task"
	if b.app.chatInference == "copilot" || b.app.chatInference == "foundry" {
		runtime, tasks = "Host Copilot CLI · "+b.app.copilotModel, "Copilot prompts with KMX tool execution; no Orka Task created"
		if b.app.chatInference == "foundry" {
			runtime, tasks = "Host Foundry client · Entra login", "Native model tool calls with KMX HTTP tool execution; no Orka Task created"
		}
		supported, _, err := b.copilotTools(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(supported))
		for _, tool := range supported {
			names = append(names, tool.Name)
		}
		tools = fmt.Sprintf("(%d tools enabled via host adapter) %s", len(names), strings.Join(names, ", "))
	}
	return []cliui.Field{
		{Label: "Location", Value: location},
		{Label: "Deployment", Value: "Orka Agent/" + b.agent + " | namespace " + b.namespace},
		{Label: "Runtime", Value: runtime},
		{Label: "Tasks", Value: tasks},
		{Label: "Tools", Value: tools},
		{Label: "Inference", Value: b.inferenceLabel()},
	}, nil
}

func (b *orkaChatBackend) Send(ctx context.Context, message string, renderer *chatRenderer) error {
	renderer.beginAssistant(b.agent)
	inference, host, err := b.app.hostInferenceStrategy()
	if err != nil {
		return err
	}
	if host {
		return b.sendHostTurn(ctx, message, renderer, inference)
	}
	var profile *orkaTaskProfile
	if renderer.verboseEnabled() {
		profile = &orkaTaskProfile{}
	}
	if b.resultSession != nil {
		deadline, _ := b.resultSession.ctx.Deadline()
		if b.resultSession.ctx.Err() != nil || time.Until(deadline) < 5*time.Minute {
			b.resultSession.close()
			b.resultSession = nil
		}
	}
	sessionStarted := time.Now()
	openedSession := false
	if b.resultSession == nil {
		parent := b.chatContext
		if parent == nil {
			parent = ctx
		}
		sessionCtx, cancel := context.WithTimeout(parent, 8*time.Minute)
		quiet := *b.app
		quiet.Err = io.Discard
		session, err := quiet.openOrkaResultSession(sessionCtx, quickstartResultOptions(b.namespace, ""))
		if err != nil {
			cancel()
			return err
		}
		context.AfterFunc(session.ctx, cancel)
		b.resultSession = session
		openedSession = true
	}
	sessionTime := time.Duration(0)
	if openedSession {
		sessionTime = time.Since(sessionStarted)
	}
	answer, err := b.app.runQuickstartOrkaTaskProfile(ctx, b.agent, b.namespace, message, profile, func(phase string) {
		renderer.assistantOperation(b.agent, "WORKING", "", colorBlue, phase)
	}, b.resultSession)
	if profile != nil {
		profile.session += sessionTime
	}
	if err != nil {
		return err
	}
	renderer.assistant(b.agent, answer, true)
	if profile != nil {
		renderer.assistantOperation(b.agent, "TIMING", "", colorBlue, profile.summary())
	}
	return nil
}

func (a *App) runQuickstartOrkaTaskProfile(parent context.Context, agent, namespace, prompt string, profile *orkaTaskProfile, report func(string), reuse ...*orkaResultSession) (string, error) {
	phase := func(label string) {
		if report != nil {
			report(label)
		}
	}
	suffix, err := randomHex(8)
	if err != nil {
		return "", err
	}
	prefix := strings.TrimRight(agent[:min(len(agent), 40)], "-")
	doc := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"name": prefix + "-" + suffix, "namespace": namespace},
		"spec":     map[string]any{"type": "ai", "prompt": prompt, "agentRef": map[string]any{"name": agent, "namespace": namespace}, "resources": map[string]any{}},
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	opt := quickstartResultOptions(namespace, prompt)
	quiet := *a
	var diagnostics bytes.Buffer
	quiet.Err = &diagnostics
	phase("Opening result connection")
	started := time.Now()
	var session *orkaResultSession
	if len(reuse) > 0 {
		session = reuse[0]
	} else {
		session, err = quiet.openOrkaResultSession(ctx, opt)
		if err != nil {
			return "", err
		}
		defer session.close()
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	unhook := context.AfterFunc(session.ctx, stop)
	defer unhook()
	phase("Checking result access")
	if err := session.probeBeforeCreate(ctx, namespace, orkaObjectName(doc), "no Task created; earlier setup may remain"); err != nil {
		return "", err
	}
	if profile != nil {
		profile.session = time.Since(started)
	}
	phase("Creating Task")
	started = time.Now()
	id, err := a.createOrkaObject(ctx, namespace, doc)
	if err != nil {
		return "", err
	}
	if profile != nil {
		profile.create = time.Since(started)
	}
	phase("Task " + id.Name + ": waiting for worker execution and completion")
	started = time.Now()
	answer, err := a.waitOrkaTaskResultProgress(ctx, namespace, id, session, func() {
		if profile != nil {
			profile.execution = time.Since(started)
		}
		started = time.Now()
		phase("Retrieving result")
	})
	if err == nil && profile != nil {
		profile.result = time.Since(started)
		started = time.Now()
		profile.loadEvents(session.ctx, session, namespace, id.Name)
		profile.telemetry = time.Since(started)
	}
	return answer, err
}
