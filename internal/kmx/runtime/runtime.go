// Package runtime defines the platform-neutral contract used by KMX chat.
// Platform identity, execution engine and model provider are separate concepts.
// No Kubernetes, terminal or vendor SDK types cross this boundary.
package runtime

import "context"

type ID string

const (
	Orka ID = "orka"
)

type AgentRef struct {
	Runtime                             ID
	Context, Namespace, Kind, Name, UID string
}

// Capabilities is a static declaration, not negotiation. The chat flags come
// from the session contract; the lifecycle flags declare which LifecycleAdapter
// verbs a runtime implements, and an undeclared verb returns
// *UnsupportedVerbError instead of being attempted.
type Capabilities struct {
	Streaming, Resume, Approvals, EditTools, SwitchAgent, Lift, SelectInference bool
	Render, Deploy, Status, Evaluate                                            bool
}

type Command struct {
	Name, Usage string
}

type Field struct{ Label, Value string }
type Status struct {
	Agent  AgentRef
	Fields []Field
}

type EventKind string

const (
	AgentStarted   EventKind = "agent"
	Text           EventKind = "assistant"
	Operation      EventKind = "operation"
	AgentOperation EventKind = "agent-operation"
	Timing         EventKind = "timing"
	Connection     EventKind = "status"
)

// Events are observations, not terminal success. Only Send returning nil means
// the adapter's runtime-specific result checks completed successfully.
type Event struct {
	Kind               EventKind
	Agent, Label, Text string
	Start              bool
}
type Emit func(Event)
type Turn struct {
	Message string
	Verbose bool
}

type Session interface {
	Agent() AgentRef
	Capabilities() Capabilities
	Commands() []Command
	Connect(context.Context, Emit) (Status, error)
	Send(context.Context, Turn, Emit) error
	Close()
}

// Discovery adapters distinguish absence from unreadable/unsupported objects.
// Callers must not turn an error into fallback to a different runtime.
type Target struct{ Context, Namespace, Name string }
type Probe struct {
	Found bool
	Agent AgentRef
}
type Adapter interface {
	ID() ID
	Probe(context.Context, Target) (Probe, error)
	Open(context.Context, Target) (Session, error)
}

// Configuration is optional. UI-specific editors/pickers are supplied by the
// application coordinator; Session itself never owns terminal input.
type CommandResult struct{ ResetConversation, Refresh bool }
type CommandHandler interface {
	Execute(context.Context, string, Emit) (CommandResult, error)
}
