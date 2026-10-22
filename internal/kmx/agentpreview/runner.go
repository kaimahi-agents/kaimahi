// Package agentpreview defines the embedded runner boundary, not an execution engine.
package agentpreview

import (
	"context"
	"errors"

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
}

type State string

const (
	Complete  State = "complete"
	Cancelled State = "cancelled"
	Failed    State = "error"
	NotRun    State = "not_run"
)

var (
	ErrJournalFailed    = errors.New("preview journal finalization failed")
	ErrModelUnavailable = errors.New("preview model unavailable")
)

// Completion contains private bounded execution data, never a JSON projection.
type Completion struct {
	State        State
	ID           string
	FileRevision string
	Provider     string
	Model        string
	Answer       string
}
type ExampleOutcome struct {
	ID         string
	Completion Completion
	Assertions []runtime.EvaluationAssertionResult
}
type Outcome struct {
	// NoJournal explicitly identifies the temporary direct preview, never evidence.
	NoJournal bool
	State     State
	AdHoc     *Completion
	Examples  []ExampleOutcome
	Journal   string
}
