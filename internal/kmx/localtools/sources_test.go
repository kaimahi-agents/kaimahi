package localtools

import (
	"encoding/json"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

func TestCapturingSourceOnlyRecordedLiveAndLegacyBytes(t *testing.T) {
	defs := []api.ToolDefinition{{Name: "ping", InputSchema: map[string]any{"type": "object"}}}
	for _, mode := range []string{"live", "recorded"} {
		for _, source := range []string{"live", "recorded", "unavailable"} {
			config := SourceConfig{Version: 1, Tools: []ToolSource{{Name: "ping", Mode: mode, Source: source, Capturing: true}}}
			if got := config.Validate(defs) == nil; got != (mode == "recorded" && source == "live") {
				t.Fatalf("capturing %s/%s valid=%v", mode, source, got)
			}
		}
	}
	legacy := `{"version":1,"tools":[{"name":"ping","mode":"recorded","source":"live"}]}`
	var config SourceConfig
	if json.Unmarshal([]byte(legacy), &config) != nil || config.Validate(defs) != nil {
		t.Fatal("legacy source refused")
	}
	raw, err := json.Marshal(config)
	if err != nil || string(raw) != legacy {
		t.Fatalf("legacy encoding %s: %v", raw, err)
	}
}
