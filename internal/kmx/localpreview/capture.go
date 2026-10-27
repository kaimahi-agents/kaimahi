package localpreview

import (
	"encoding/json"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Only the successful live dispatcher creates observations. Journaled output,
// including a successful unavailable answer, cannot create this provenance.
type captureObservation struct {
	session  string
	exchange localtools.ToolExchange
}

func copyExchange(value localtools.ToolExchange) (localtools.ToolExchange, error) {
	raw, err := json.Marshal(value)
	var copy localtools.ToolExchange
	if err != nil || !runtime.JSONNumbersExact(raw) || json.Unmarshal(raw, &copy) != nil {
		return copy, agentpreview.ErrUnsafeToolContent
	}
	return copy, nil
}

func replayCapture(call api.ToolCall, arguments, output map[string]any) (api.ToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	actual := call.Args
	if actual == nil {
		actual = map[string]any{}
	}
	a, err := json.Marshal(actual)
	b, other := json.Marshal(arguments)
	if err != nil || other != nil || string(a) != string(b) {
		return api.ToolResult{}, agentpreview.ErrRecordingMismatch
	}
	copy, err := copyExchange(localtools.ToolExchange{Result: api.ToolResult{ID: call.ID, Output: output}})
	return copy.Result, err
}

// records must have passed both strict replay and submitted config/input binding.
// This second join binds them to the trusted live execution, not just selection.
func (p toolPlan) recordings(example string, execution Execution) ([]agentfile.Recording, error) {
	var out []agentfile.Recording
	for _, source := range p.sources.Tools {
		if !source.Capturing {
			continue
		}
		observed, found := p.captured[source.Name]
		if execution.ToolCallCounts[source.Name] == 0 && !found {
			continue
		}
		if !found || source.Source != "live" || source.Mode != "recorded" || execution.ToolCallCounts[source.Name] == 0 || !execution.Journaled || !execution.ToolEvidenceComplete || observed.session == "" || observed.session != execution.SessionUID {
			return nil, errRunnerExecution
		}
		expected, err := json.Marshal(observed.exchange)
		if err != nil {
			return nil, errRunnerExecution
		}
		matches := 0
		for _, exchange := range execution.ToolExchanges {
			if exchange.Call.ID == observed.exchange.Call.ID {
				actual, err := json.Marshal(exchange)
				if err != nil || string(actual) != string(expected) {
					return nil, errRunnerExecution
				}
				matches++
			}
		}
		if matches != 1 {
			return nil, errRunnerExecution
		}
		for _, definition := range p.definitions {
			if definition.Name != source.Name {
				continue
			}
			arguments := observed.exchange.Call.Args
			if arguments == nil {
				arguments = map[string]any{}
			}
			tool := agentfile.MockTool{Name: source.Name, Mode: "recorded", Description: definition.Description, Parameters: definition.InputSchema, Calls: []agentfile.MockCall{{Arguments: arguments, Result: observed.exchange.Result.Output}}}
			raw, err := json.Marshal(tool)
			var detached agentfile.MockTool
			if err != nil || !runtime.JSONNumbersExact(raw) || json.Unmarshal(raw, &detached) != nil {
				return nil, agentpreview.ErrUnsafeToolContent
			}
			out = append(out, agentfile.Recording{ExampleID: example, Tool: detached})
		}
	}
	return out, nil
}
