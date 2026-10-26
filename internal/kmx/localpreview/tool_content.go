package localpreview

import (
	"strconv"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// Recorded fixture payloads are scanned from their own root. These are trusted
// absolute paths in each marshaled shape (Mocks uses lowercase JSON tags; the
// Sessions API types and agentfile.Agent use their default field names).
var (
	mockPayloadPaths  = [][]string{{"tools", "*", "calls", "*", "arguments"}, {"tools", "*", "calls", "*", "result"}}
	agentPayloadPaths = [][]string{
		{"Agent", "Examples", "*", "Mocks", "tools", "*", "calls", "*", "arguments"},
		{"Agent", "Examples", "*", "Mocks", "tools", "*", "calls", "*", "result"},
	}
	callPayloadPaths      = [][]string{{"Args"}}
	resultPayloadPaths    = [][]string{{"Output"}}
	modelCallPayloadPath  = [][]string{{"Message", "Parts", "*", "ToolCall", "Args"}}
	wireReplyPayloadPaths = [][]string{{"choices", "*", "message", "tool_calls", "*", "function", "arguments"}}
)

// ToolModel validates the fixed system/user/call/results message order before
// scanning. Only call arguments and tool-result JSON get payload roots, never
// JSON-looking system instructions or user input.
func wireRequestPayloadPaths(messages int) [][]string {
	paths := [][]string{{"messages", "2", "tool_calls", "*", "function", "arguments"}}
	for i := 3; i < messages; i++ {
		paths = append(paths, []string{"messages", strconv.Itoa(i), "content"})
	}
	return paths
}

func checkToolContent(check func(...string) error, r agentpreview.Request) error {
	a := r.Agent
	fields := []string{r.FileLabel, a.Title, a.Identity.ID, a.Identity.Purpose, a.Revision, a.Model, a.Instructions, a.NativeTaskInstructions, r.Route.Provider, r.Route.Model}
	fields = append(fields, a.Tools...)
	if r.JudgeRoute != nil {
		fields = append(fields, r.JudgeRoute.Provider, r.JudgeRoute.Model)
	}
	if r.Input != nil {
		fields = append(fields, *r.Input)
	}
	for _, ex := range r.Examples {
		if localtools.CheckJSONPayloads(ex.Mocks, check, mockPayloadPaths...) != nil {
			return agentpreview.ErrUnsafeToolContent
		}
		fields = append(fields, ex.ID, ex.Input)
		for _, as := range ex.Assertions {
			fields = append(fields, as.ID, as.Type, as.Value, as.Pattern, as.Tool, as.Rubric, as.JudgeModel, as.JudgeVersion)
		}
	}
	return check(fields...)
}
