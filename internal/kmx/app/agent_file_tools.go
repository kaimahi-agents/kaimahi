package app

import (
	"slices"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// Older runners omit source metadata; absence never asserts a live call.
func validPreviewTools(c agentpreview.Completion, ex agentfile.Example, names []string) bool {
	if len(c.Tools) == 0 {
		return ex.Mocks == nil
	}
	if len(c.Tools) != len(names) {
		return false
	}
	seen := map[string]bool{}
	total := 0
	for _, row := range c.Tools {
		if !slices.Contains(names, row.Name) || seen[row.Name] || row.CallCount < 0 || row.CallCount > localtools.MaxCalls || row.RecordingSaved {
			return false
		}
		mode := "live"
		frozen := false
		if ex.Mocks != nil {
			for _, tool := range ex.Mocks.Tools {
				if tool.Name == row.Name {
					mode = tool.Mode
					frozen = len(tool.Calls) > 0
				}
			}
		}
		if row.Mode != mode || frozen && row.Source != "recorded" || !frozen && row.Source != "live" && row.Source != "unavailable" {
			return false
		}
		seen[row.Name] = true
		total += row.CallCount
	}
	return total == c.ToolCalls
}
