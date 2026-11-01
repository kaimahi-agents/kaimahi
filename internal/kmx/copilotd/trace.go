// Package copilotd runs checked file cases through the host's native agent loop.
package copilotd

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/microsoft/agent-host-protocol/clients/go/ahp"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
)

var errTrace = errors.New("native execution evidence is incomplete")

type nativeCall struct {
	name, input string
	complete    bool
}
type trace struct {
	mu                sync.Mutex
	chat, turn, model string
	state             ahptypes.ChatState
	calls             map[string]*nativeCall
	models            []string
	size              int
	seq               int64
	done              chan struct{}
	ended             bool
	err               error
}

func newTrace(chat, turn, model string, state ahptypes.ChatState) *trace {
	return &trace{chat: chat, turn: turn, model: model, state: state, calls: map[string]*nativeCall{}, done: make(chan struct{})}
}

// Runs synchronously before AHP's lossy fan-out. Never hold mu across RPC or IO.
func (t *trace) accept(e ahptypes.ActionEnvelope) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if string(e.Channel) != t.chat || e.RejectionReason != nil {
		return nil
	}
	b, err := json.Marshal(e.Action)
	if err != nil {
		return t.fail(errTrace)
	}
	// Required turnId fields survive typed marshaling even when empty;
	// genuinely chat-scoped actions have no such field.
	var id struct{ TurnId *string }
	if json.Unmarshal(b, &id) != nil {
		return t.fail(errTrace)
	}
	if id.TurnId != nil {
		if *id.TurnId == "" {
			return t.fail(errTrace)
		}
		if *id.TurnId != t.turn {
			return nil
		}
	}
	if t.err != nil {
		return t.err
	}
	t.size += len(b)
	if t.size > 8<<20 || e.ServerSeq <= t.seq {
		return t.fail(errTrace)
	}
	t.seq = e.ServerSeq
	switch a := e.Action.Value.(type) {
	case *ahptypes.ChatUsageAction:
		if a.Usage.Model == nil || *a.Usage.Model == "" {
			return t.fail(errTrace)
		}
		found := false
		for _, m := range t.models {
			found = found || m == *a.Usage.Model
		}
		if !found {
			t.models = append(t.models, *a.Usage.Model)
		}
		if *a.Usage.Model != t.model {
			return t.fail(agentpreview.ErrModelUnavailable)
		}
	case *ahptypes.ChatToolCallStartAction:
		if !localtools.ValidToolName(a.ToolName) || a.ToolCallId == "" || t.calls[a.ToolCallId] != nil {
			return t.fail(errTrace)
		}
		t.calls[a.ToolCallId] = &nativeCall{name: a.ToolName}
		// Contributing child execution is not provable from the parent's trace.
		if a.ToolName == "task" || a.ToolName == "agent" {
			return t.fail(errTrace)
		}
	case *ahptypes.ChatToolCallReadyAction:
		c := t.calls[a.ToolCallId]
		if c == nil {
			return t.fail(errTrace)
		}
		if a.ToolInput != nil && a.ToolInput.Inline != nil {
			c.input = *a.ToolInput.Inline
		}
	case *ahptypes.ChatToolCallCompleteAction:
		c := t.calls[a.ToolCallId]
		if c == nil || c.complete {
			return t.fail(errTrace)
		}
		c.complete = true
	case *ahptypes.ChatTurnCompleteAction:
		t.end()
	case *ahptypes.ChatTurnCancelledAction, *ahptypes.ChatErrorAction, *ahptypes.ChatTruncatedAction, *ahptypes.StateActionUnknown:
		return t.fail(errTrace)
	}
	ahp.ApplyActionToChat(&t.state, e.Action)
	return nil
}
func (t *trace) end() {
	if !t.ended {
		close(t.done)
		t.ended = true
	}
}
func (t *trace) fail(err error) error         { t.err = err; t.end(); return err }
func (t *trace) snapshot() ahptypes.ChatState { t.mu.Lock(); defer t.mu.Unlock(); return t.state }
func (t *trace) pending() (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.calls {
		if !c.complete {
			return c.name, c.input
		}
	}
	return "", ""
}
func (t *trace) observed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.models...)
}
func (t *trace) finish(state *ahptypes.ChatState) (string, []string, map[string]int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return "", t.models, nil, t.err
	}
	if !t.ended || len(t.models) == 0 || state == nil {
		return "", t.models, nil, errTrace
	}
	var turn *ahptypes.Turn
	for i := range state.Turns {
		if state.Turns[i].Id == t.turn {
			turn = &state.Turns[i]
		}
	}
	if turn == nil || turn.State != ahptypes.TurnStateComplete {
		return "", t.models, nil, errTrace
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	var answer strings.Builder
	for _, part := range turn.ResponseParts {
		switch p := part.Value.(type) {
		case *ahptypes.MarkdownResponsePart:
			answer.WriteString(p.Content)
		case *ahptypes.ToolCallResponsePart:
			b, _ := json.Marshal(p.ToolCall)
			var c struct{ ToolCallId, ToolName, Status string }
			if json.Unmarshal(b, &c) != nil {
				return "", t.models, nil, errTrace
			}
			got := t.calls[c.ToolCallId]
			if got == nil || !got.complete || seen[c.ToolCallId] || got.name != c.ToolName || c.Status != "completed" {
				return "", t.models, nil, errTrace
			}
			seen[c.ToolCallId] = true
			counts[c.ToolName]++
		case *ahptypes.ResourceResponsePart, *ahptypes.ResponsePartUnknown:
			return "", t.models, nil, errTrace
		}
	}
	if len(seen) != len(t.calls) || answer.Len() > 256<<10 {
		return "", t.models, nil, errTrace
	}
	return answer.String(), append([]string(nil), t.models...), counts, nil
}
