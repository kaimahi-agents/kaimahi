package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeployConsoleCreationUsesCurrentLabels(t *testing.T) {
	p := &agentTUICreatePane{stage: "plan", summary: ReleaseLiftSummary{Member: "lift"}}
	lines := strings.Join(p.reviewLines(100), "\n")
	if !strings.Contains(lines, "DEPLOY PLAN / IDENTITY REVIEW") || strings.Contains(lines, "LIFT PLAN") || !strings.Contains(lines, `"member": "lift"`) {
		t.Fatalf("plan label or opaque identity changed: %s", lines)
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.width, m.height, m.creation = 110, 40, p
	p.running = true
	view := m.creationView()
	if !strings.Contains(view, "SOURCE → EVAL → DEPLOY") || strings.Contains(view, "SOURCE → EVAL → LIFT") {
		t.Fatalf("creation header advertises old verb: %s", view)
	}
}

// Table copy is current CLI guidance, while even descriptive JSON values are
// historical machine data. Formatting must not mutate the underlying report.
func TestDeployTargetsTableKeepsMachineReport(t *testing.T) {
	var table, wire bytes.Buffer
	a := &App{Out: &table}
	if err := a.Targets(TargetsOptions{Output: "table"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(table.String(), "lift") || !strings.Contains(table.String(), "deploy: supported") || !strings.Contains(table.String(), "deploy requires explicit evidence") {
		t.Fatalf("table advertises retired spelling: %s", &table)
	}
	a.Out = &wire
	if err := a.Targets(TargetsOptions{Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var report TargetsReport
	if err := json.Unmarshal(wire.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), `"operation":"deploy"`) || !strings.Contains(wire.String(), `"operation":"lift"`) || !strings.Contains(wire.String(), "workflow; lift requires explicit evidence") || !strings.Contains(wire.String(), "immutable digest lift") {
		t.Fatalf("machine keys or values changed: %s", &wire)
	}
}
