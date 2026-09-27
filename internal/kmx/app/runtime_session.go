package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// This bridge keeps renderer details in app while adapters expose typed events.
// Existing protocol parsers may write to a renderer internally during migration;
// neither the Session contract nor a third-party adapter needs to know that.
func runtimeEventRenderer(emit agentruntime.Emit, verbose bool) *chatRenderer {
	r := newChatRenderer(io.Discard)
	r.verbose = verbose
	r.timeline = func(e chatTimelineEvent) {
		emit(agentruntime.Event{Kind: agentruntime.EventKind(e.kind), Agent: e.agent, Label: e.label, Text: e.text, Start: e.start})
	}
	return r
}

func emitToRenderer(r *chatRenderer) agentruntime.Emit {
	return func(e agentruntime.Event) {
		switch e.Kind {
		case agentruntime.AgentStarted:
			r.beginAssistant(e.Agent)
		case agentruntime.Text:
			r.assistant(e.Agent, e.Text, e.Start)
		case agentruntime.AgentOperation:
			r.assistantOperation(e.Agent, e.Label, "", colorBlue, e.Text)
		case agentruntime.Operation:
			r.operation(e.Label, "", colorBlue, e.Text)
		case agentruntime.Connection:
			r.statusStart(e.Agent, e.Text)
		case agentruntime.Timing:
			r.operation("TIMING", "", colorBlue, e.Text)
		}
	}
}

// runtimeChatBackend is the presentation adapter used by the existing terminal
// drivers. Production sessions supply all platform operations through Session.
type runtimeChatBackend struct {
	session   agentruntime.Session
	configure func(context.Context, string, *chatRenderer) (bool, error)
}

func (b *runtimeChatBackend) Agent() string { return b.session.Agent().Name }
func (b *runtimeChatBackend) Close()        { b.session.Close() }
func (b *runtimeChatBackend) Connect(ctx context.Context, r *chatRenderer) ([]cliui.Field, error) {
	status, err := b.session.Connect(ctx, emitToRenderer(r))
	if err != nil {
		return nil, err
	}
	fields := make([]cliui.Field, 0, len(status.Fields))
	for _, field := range status.Fields {
		fields = append(fields, cliui.Field{Label: field.Label, Value: field.Value})
	}
	return fields, nil
}
func (b *runtimeChatBackend) Send(ctx context.Context, message string, r *chatRenderer) error {
	return b.session.Send(ctx, agentruntime.Turn{Message: message, Verbose: r.verboseEnabled()}, emitToRenderer(r))
}
func (b *runtimeChatBackend) ChatCommands() []slashCommand {
	commands := commonChatCommands()
	for _, command := range b.session.Commands() {
		commands = append(commands, slashCommand{name: command.Name, usage: command.Usage})
	}
	return commands
}
func (b *runtimeChatBackend) Configure(ctx context.Context, command string, r *chatRenderer) (bool, error) {
	if !containsChatCommand(b.ChatCommands(), command) {
		return false, fmt.Errorf("command %s is not supported by %s", command, b.session.Agent().Runtime)
	}
	if handler, ok := b.session.(agentruntime.CommandHandler); ok {
		result, err := handler.Execute(ctx, command, emitToRenderer(r))
		return result.ResetConversation, err
	}
	if b.configure != nil {
		return b.configure(ctx, command, r)
	}
	return false, fmt.Errorf("configuration is not supported by %s", b.session.Agent().Runtime)
}

func commonChatCommands() []slashCommand {
	return []slashCommand{
		{name: "/help", usage: "/help — available commands"}, {name: "/retry", usage: "/retry — repeat last message"},
		{name: "/exit", usage: "/exit — leave chat"}, {name: "/verbose-off", usage: "/verbose-off — hide details"}, {name: "/verbose-on", usage: "/verbose-on — show details"},
	}
}
func chatCommandsSummary(commands []slashCommand) string {
	var names []string
	for _, command := range commands {
		names = append(names, command.name)
	}
	return strings.Join(names, " ")
}
func chatCommandsHelp(commands []slashCommand) string {
	var lines []string
	for _, command := range commands {
		lines = append(lines, command.usage)
	}
	return strings.Join(lines, "\n")
}
func containsChatCommand(commands []slashCommand, name string) bool {
	for _, command := range commands {
		if command.name == name {
			return true
		}
	}
	return false
}
func chatBackendCommands(backend interactiveChatBackend) []slashCommand {
	if source, ok := backend.(interface{ ChatCommands() []slashCommand }); ok {
		return source.ChatCommands()
	}
	return commonChatCommands()
}

type orkaRuntimeSession struct{ backend *orkaChatBackend }

func (s *orkaRuntimeSession) Agent() agentruntime.AgentRef {
	return agentruntime.AgentRef{Runtime: agentruntime.Orka, Context: s.backend.app.Cfg.KubeContext, Namespace: s.backend.namespace, Kind: "agents.core.orka.ai", Name: s.backend.agent}
}
func (s *orkaRuntimeSession) Capabilities() agentruntime.Capabilities {
	return agentruntime.Capabilities{EditTools: true, SwitchAgent: true, Lift: true, SelectInference: true}
}
func (s *orkaRuntimeSession) Commands() []agentruntime.Command {
	var commands []agentruntime.Command
	capabilities := s.Capabilities()
	for _, command := range orkaSlashCommands {
		enabled := false
		switch command.name {
		case "/agent":
			enabled = capabilities.SwitchAgent
		case "/tools":
			enabled = capabilities.EditTools
		case "/lift":
			enabled = capabilities.Lift
		case "/inference", "/inference-local", "/inference-copilot", "/inference-foundry":
			enabled = capabilities.SelectInference
		}
		if enabled {
			commands = append(commands, agentruntime.Command{Name: command.name, Usage: command.usage})
		}
	}
	return commands
}

var _ agentruntime.Session = (*orkaRuntimeSession)(nil)
var _ agentruntime.Adapter = orkaRuntimeAdapter{}

func (s *orkaRuntimeSession) Connect(ctx context.Context, emit agentruntime.Emit) (agentruntime.Status, error) {
	fields, err := s.backend.Connect(ctx, runtimeEventRenderer(emit, false))
	status := agentruntime.Status{Agent: s.Agent()}
	for _, f := range fields {
		status.Fields = append(status.Fields, agentruntime.Field{Label: f.Label, Value: f.Value})
	}
	return status, err
}
func (s *orkaRuntimeSession) Send(ctx context.Context, turn agentruntime.Turn, emit agentruntime.Emit) error {
	return s.backend.Send(ctx, turn.Message, runtimeEventRenderer(emit, turn.Verbose))
}
func (s *orkaRuntimeSession) Close() { s.backend.Close() }
