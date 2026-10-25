package localtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

var errTrace = errors.New("local tools: unsupported or incomplete preview trace")
var callID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// MaxCalls bounds the first-response batch; the final agent send is text-only.
const MaxCalls = 5

// Harness is the versioned, secret-free bounded preview contract. Bindings and
// credentials belong exclusively to the host's executor, never this config.
type Harness struct {
	Model       string
	Definitions []api.ToolDefinition
	Sources     *SourceConfig
}

type Config struct {
	Instructions string               `json:"instructions"`
	Definitions  []api.ToolDefinition `json:"definitions,omitempty"`
	Sources      *SourceConfig        `json:"sources,omitempty"`
}

func (h Harness) Describe(context.Context) (api.Descriptor, error) {
	defs := h.Definitions
	if defs == nil {
		defs = []api.ToolDefinition{Definition()}
	}
	tools := make([]api.ToolSpec, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, api.ToolSpec{Name: d.Name, Mediation: api.MediationControllerMediated})
	}
	return api.Descriptor{ID: HarnessName, Version: "1", Models: []string{h.Model}, Tools: tools, Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay}}, nil
}

func (h Harness) Run(ctx context.Context, s *api.Start, sink api.EventSink) error {
	if s == nil || s.ExecutionID == "" || len(s.History) != 0 || len(s.Inputs) != 1 || !textMessage(&s.Inputs[0], "user") || !runtime.JSONKeysUnique(s.Config) {
		return errTrace
	}
	var config Config
	d := json.NewDecoder(bytes.NewReader(s.Config))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || config.Instructions == "" {
		return errTrace
	}
	defs := []api.ToolDefinition{Definition()}
	if config.Definitions != nil {
		var err error
		defs, err = CopyDefinitions(config.Definitions)
		if err != nil {
			return errTrace
		}
	}
	if h.Definitions != nil {
		trusted, err := CopyDefinitions(h.Definitions)
		if err != nil || !reflect.DeepEqual(trusted, defs) {
			return errTrace
		}
	}
	if config.Sources.Validate(defs) != nil || h.Sources != nil && !reflect.DeepEqual(h.Sources, config.Sources) {
		return errTrace
	}
	messages := []api.Message{*api.TextMessage("system", config.Instructions), s.Inputs[0]}
	for round := 0; round < 2; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := sink.Model(ctx, api.ModelRequest{Model: h.Model, Messages: messages, Tools: defs})
		if err != nil {
			return err
		}
		m := response.Message
		if textMessage(&m, "assistant") {
			return nil
		}
		if round != 0 || m.Role != "assistant" || len(m.Parts) == 0 || len(m.Parts) > MaxCalls {
			return errTrace
		}
		// Validate the entire batch before starting its first operation.
		seen := map[string]bool{}
		for _, p := range m.Parts {
			c := p.ToolCall
			if c == nil || p.Text != nil || p.Data != nil || p.File != nil || p.Reasoning != nil || p.ToolResult != nil || !callID.MatchString(c.ID) || seen[c.ID] || ValidateArguments(defs, c.Tool, c.Args) != nil || c.Mediation != "" || c.IdempotencyKey != "" {
				return errTrace
			}
			seen[c.ID] = true
		}
		messages = append(messages, m)
		for _, p := range m.Parts {
			// Copy: never mutate the journaled response or its replay hash.
			call := *p.ToolCall
			call.Mediation = api.MediationControllerMediated
			call.IdempotencyKey = s.ExecutionID + ":" + call.ID
			if err := ctx.Err(); err != nil {
				return err
			}
			result, err := sink.ToolCall(ctx, call)
			if err != nil {
				return err
			}
			if result.ID != call.ID || result.IsError || result.Error != "" || result.OutputURI != "" || result.OutputDigest != "" || result.Content != nil || result.Output == nil {
				return errTrace
			}
			raw, err := json.Marshal(result.Output)
			if err != nil || len(raw) > 32*1024 {
				return errTrace
			}
			messages = append(messages, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &result}}})
		}
	}
	return errTrace
}

func textMessage(m *api.Message, role string) bool {
	if m == nil || m.Role != role || len(m.Parts) != 1 {
		return false
	}
	p := m.Parts[0]
	return p.Text != nil && p.File == nil && p.Data == nil && p.Reasoning == nil && p.ToolCall == nil && p.ToolResult == nil && len(p.Text.Text) > 0 && len(p.Text.Text) <= 256*1024 && utf8.ValidString(p.Text.Text)
}

// Trace is preview-only content and safe counts, not portable suite evidence.
type Trace struct {
	Answer      string
	ToolCalls   int
	CallsByName map[string]int
	Sources     *SourceConfig
}

// VerifyTrace accepts only one complete bounded invocation, then reconstructs
// it on an isolated log. No credential or live provider is available on replay.
func VerifyTrace(ctx context.Context, records []eventlog.Record, model string) (Trace, error) {
	var out Trace
	kinds := []api.EventKind{api.EventExecutionStart, api.EventInput, api.EventModelCall, api.EventOutput, api.EventEnd}
	if len(records) >= 9 && len(records) <= 7+2*MaxCalls && len(records)%2 == 1 {
		out.ToolCalls = (len(records) - 7) / 2
		out.CallsByName = map[string]int{}
		kinds = kinds[:4]
		seen := map[string]bool{}
		for i := 0; i < out.ToolCalls; i++ {
			call, result := records[4+2*i].Event.ToolCall, records[5+2*i].Event.Result
			if call == nil || result == nil || !callID.MatchString(call.ID) || seen[call.ID] || result.ID != call.ID {
				return Trace{}, errTrace
			}
			seen[call.ID] = true
			out.CallsByName[call.Tool]++
			kinds = append(kinds, api.EventToolCall, api.EventToolResult)
		}
		kinds = append(kinds, api.EventModelCall, api.EventOutput, api.EventEnd)
	}
	if len(records) != len(kinds) {
		return Trace{}, errTrace
	}
	execution := records[0].Event.ExecutionID
	if execution == "" {
		return Trace{}, errTrace
	}
	total := 0
	for i, r := range records {
		e := r.Event
		encoded, err := json.Marshal(e)
		total += len(encoded)
		if err != nil || total > 4*1024*1024 || e.Kind != kinds[i] || e.ExecutionID != execution || e.SchemaVersion < 0 || e.SchemaVersion > 1 {
			return Trace{}, errTrace
		}
		if e.Kind == api.EventModelCall && (e.ModelCall == nil || e.ModelCall.Model != model || len(e.ModelCall.InputHash) != 64 || len(e.ModelCall.Params) != 0) {
			return Trace{}, errTrace
		}
	}
	s := records[0].Event.ExecutionStart
	end := records[len(records)-1].Event.End
	if s == nil || s.Harness != HarnessName || s.HarnessVersion != "1" || s.ResumeFromSeq != 0 || s.InputCount == nil || *s.InputCount != 1 || !textMessage(records[1].Event.Message, "user") || end == nil || end.State != "COMPLETED" || end.Error != nil {
		return Trace{}, errTrace
	}
	final := records[len(records)-2].Event.Message
	if !textMessage(final, "assistant") {
		return Trace{}, errTrace
	}
	calls := 0
	failModel := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errTrace
	}
	failTool := func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		calls++
		return api.ToolResult{}, errTrace
	}
	replay, err := controller.New(eventlog.AsStore(eventlog.NewFrom(records)), failModel, controller.WithSessionUID("offline-preview"), controller.WithToolExecutor(failTool))
	if err != nil {
		return Trace{}, errTrace
	}
	outputs, err := replay.Replay(ctx, Harness{Model: model})
	if ctx.Err() != nil {
		return Trace{}, ctx.Err()
	}
	wantOutputs := 1
	if out.ToolCalls > 0 {
		wantOutputs = 2
	}
	if err != nil || calls != 0 || replay.ModelInvocations() != 0 || len(outputs) != wantOutputs || outputs[len(outputs)-1] != final.Text() {
		return Trace{}, errTrace
	}
	var config Config
	if json.Unmarshal(s.Config, &config) != nil {
		return Trace{}, errTrace
	}
	out.Sources = config.Sources
	out.Answer = final.Text()
	return out, nil
}
