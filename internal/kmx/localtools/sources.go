package localtools

import "github.com/agent-primitives/sessions/api"

// SourceConfig records dispatch selection, not proof of execution or capture.
// Absence preserves legacy journal bytes and means source was not recorded.
type SourceConfig struct {
	Version int          `json:"version"`
	Tools   []ToolSource `json:"tools"`
}

type ToolSource struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Source    string `json:"source"`
	Capturing bool   `json:"capturing,omitempty"`
}

func (s *SourceConfig) Validate(defs []api.ToolDefinition) error {
	if s == nil {
		return nil
	}
	if s.Version != 1 || len(s.Tools) != len(defs) {
		return errTrace
	}
	seen := map[string]bool{}
	for _, tool := range s.Tools {
		if seen[tool.Name] || (tool.Mode != "live" && tool.Mode != "recorded") || (tool.Source != "live" && tool.Source != "recorded" && tool.Source != "unavailable") || tool.Mode == "live" && tool.Source == "recorded" || tool.Capturing && (tool.Mode != "recorded" || tool.Source != "live") {
			return errTrace
		}
		found := false
		for _, def := range defs {
			if def.Name == tool.Name {
				found = true
			}
		}
		if !found {
			return errTrace
		}
		seen[tool.Name] = true
	}
	return nil
}
