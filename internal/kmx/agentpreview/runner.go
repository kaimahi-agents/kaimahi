// Package agentpreview defines the request and outcome contract for agent-file
// preview runners. It supplies the runner interface, not an execution engine.
package agentpreview

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type Runner interface {
	Run(context.Context, Request) (Outcome, error)
}

type Request struct {
	Agent    *agentfile.Agent
	Examples []agentfile.Example
	Route    modelaccess.Route
	// Nil uses Route. Only the caller may choose an override.
	JudgeRoute *modelaccess.Route
	Input      *string
	Progress   func(string) error
	// ExampleFinished is synchronous, observational and runner-dependent. It
	// reports a checked case before the next starts; finalization can still fail.
	ExampleFinished func(ExampleOutcome)
	// ToolBindings and FileLabel are operator-owned host inputs, never harness config.
	ToolBindings string
	FileLabel    string
}

// Report carries content-free diagnostics; a failed sink stops later execution.
func (r Request) Report(format string, args ...any) error {
	if r.Progress == nil {
		return nil
	}
	if err := r.Progress(fmt.Sprintf(format, args...)); err != nil {
		return fmt.Errorf("%w: %w", ErrDiagnosticsFailed, err)
	}
	return nil
}

type State string

const (
	Complete  State = "complete"
	Cancelled State = "cancelled"
	Failed    State = "error"
	NotRun    State = "not_run"
)

var (
	ErrToolsUnavailable  = errors.New("host-bound local tools are unavailable")
	ErrUnsafeToolContent = errors.New("unsafe tool preview content refused")
	// ErrCredentialContent withholds all identity/content projections, including
	// caller-prefilled metadata, when a provider-owned preflight refuses them.
	ErrCredentialContent = errors.New("credential-bearing preview content refused")
	ErrJournalFailed     = errors.New("preview journal finalization failed")
	ErrModelUnavailable  = errors.New("preview model unavailable")
	ErrDiagnosticsFailed = errors.New("preview diagnostic output failed")
)

// Completion contains private bounded execution data, never a JSON projection.
type Completion struct {
	State        State
	ID           string
	FileRevision string
	Provider     string
	Model        string
	Answer       string
	ToolCalls    int
}
type ExampleOutcome struct {
	ID         string
	Completion Completion
	Assertions []runtime.EvaluationAssertionResult
}
type Outcome struct {
	// NoJournal identifies explicitly injected unjournaled runners, never evidence.
	// Canonical file previews always journal, including Copilot.
	NoJournal bool
	State     State
	AdHoc     *Completion
	Examples  []ExampleOutcome
	Journal   string
}
