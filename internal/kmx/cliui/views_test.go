package cliui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestNoColorKeepsRichHierarchyWithoutANSI(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 80})
	got := o.Actions("Next", []Action{{Label: "Inspect", Command: "kmx status"}})
	for _, want := range []string{"Next", "Inspect", "kmx status", "└"} {
		if !strings.Contains(got, want) {
			t.Errorf("rich no-color output lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("NO_COLOR layout contains ANSI: %q", got)
	}
	table := o.Table([]string{"NAME", "STATE"}, [][]string{{"agent", "ready"}})
	if strings.Contains(table, "\x1b[") {
		t.Fatalf("NO_COLOR table contains ANSI: %q", table)
	}
}

func TestLongLabelsAndValuesFitWithoutLosingBytes(t *testing.T) {
	for _, width := range []int{12, 24, 40} {
		for _, color := range []bool{false, true} {
			o := WithCapabilities(Capabilities{Rich: true, Color: color, Width: width})
			label, value := "sandboxed-workloads-with-a-long-label", "namespace/very-long-workload-identifier"
			got := o.Fields([]Field{{Label: label, Value: value}})
			for _, line := range strings.Split(got, "\n") {
				if lipgloss.Width(line) > width {
					t.Errorf("width %d: overflow %q", width, line)
				}
			}
			compact := strings.Join(strings.Fields(ansi.Strip(got)), "")
			if compact != label+value {
				t.Errorf("lost field bytes: %q", got)
			}
		}
	}
}

func TestActionsWrapProseButKeepCommandsCopyable(t *testing.T) {
	command := "kubectl --context a-long-context-name -n kagent get agents,pods"
	o := WithCapabilities(Capabilities{Rich: true, Width: 28})
	got := o.Actions("Next", []Action{{Label: "Inspect the runtime and its configuration", Detail: "This is a long explanation that should wrap", Command: command,
		Children: []Action{{Label: "Then inspect the configured model and tool seams"}},
	}})
	if !strings.Contains(got, command) {
		t.Fatalf("command bytes split: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, command) && lipgloss.Width(line) > 28 {
			t.Errorf("prose overflows: %q", line)
		}
	}
}

func TestEmptyTablesAndExplicitRoles(t *testing.T) {
	for _, width := range []int{20, 160} {
		o := WithCapabilities(Capabilities{Rich: true, Color: true, Width: width})
		if got := ansi.Strip(o.Report("Ledger", []string{"a very long header"}, nil)); !strings.Contains(got, "Ledger (0)") || !strings.Contains(got, "none") {
			t.Fatalf("empty report: %q", got)
		}
		got := o.TableWithRoles([]string{"NAME", "STATE", "CENTS"}, [][]string{{"denied", "403", "1234567"}}, []ColumnRole{ColumnText, ColumnState, ColumnNumber})
		if strings.Contains(got, o.Failure("denied")) || !strings.Contains(got, o.Failure("403")) {
			t.Errorf("state inferred from identifier or missing HTTP color: %q", got)
		}
	}
	o := WithCapabilities(Capabilities{Rich: true, Width: 80})
	got := o.TableWithRoles([]string{"STATE"}, [][]string{{"\x1b[31mdenied\x1b[0m"}}, []ColumnRole{ColumnState})
	if strings.Contains(got, "\x1b") || !strings.Contains(got, "denied") {
		t.Fatalf("NO_COLOR failed: %q", got)
	}
}

func TestStateRolesRetainMeaningInWideAndStackedTables(t *testing.T) {
	for _, width := range []int{24, 160} {
		o := WithCapabilities(Capabilities{Rich: true, Color: true, Width: width})
		for _, tc := range []struct{ value, styled string }{
			{"yes (2m ago)", o.Success("yes (2m ago)")},
			{"EXPIRED", o.Failure("EXPIRED")},
			{"denied", o.Failure("denied")},
			{"unknown", o.Warning("unknown")},
			{"EXPIRING", o.Warning("EXPIRING")},
		} {
			got := o.TableWithRoles([]string{"IDENTIFIER", "STATE"}, [][]string{{"agent-name", tc.value}}, []ColumnRole{ColumnText, ColumnState})
			if !strings.Contains(got, tc.styled) {
				t.Errorf("width %d: missing semantic style for %s: %q", width, tc.value, got)
			}
		}
	}
}

func TestFieldsAlignUnicodeByDisplayWidth(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 80})
	got := o.Fields([]Field{{Label: "界", Value: "wide"}, {Label: "id", Value: "ascii"}})
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lipgloss.Width(strings.Split(lines[0], "wide")[0]) != lipgloss.Width(strings.Split(lines[1], "ascii")[0]) {
		t.Fatalf("field values are not display-width aligned: %q", got)
	}
}

func TestTableRetainsEveryCellAndHonorsWidth(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 42})
	got := o.Table([]string{"NAME", "STATE"}, [][]string{{"hello-world", "ready"}, {"hello-tools", "attention required"}})
	for _, want := range []string{"NAME", "STATE", "hello-world", "ready", "hello-tools", "attention required"} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q: %q", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 42 {
			t.Errorf("table line is %d cells wide: %q", lipgloss.Width(line), line)
		}
	}
}

func TestTableUsesNaturalWidthOnWideTerminal(t *testing.T) {
	headers := []string{"NAME", "STATE"}
	rows := [][]string{{"hello-world", "ready"}, {"hello-tools", "attention required"}}
	wide := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 160}).Table(headers, rows)
	natural := WithCapabilities(Capabilities{Rich: true, Color: false}).Table(headers, rows)
	if wide != natural {
		t.Fatalf("wide terminal expanded a naturally sized table:\nwide:    %q\nnatural: %q", wide, natural)
	}
	if got := lipgloss.Width(wide); got >= 80 {
		t.Fatalf("compact table expanded to %d cells on a wide terminal: %q", got, wide)
	}
	lines := strings.Split(strings.Trim(wide, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("table has no data row: %q", wide)
	}
	headerState := strings.Index(lines[0], "STATE")
	rowState := strings.Index(lines[1], "ready")
	if headerState < 0 || rowState != headerState {
		t.Fatalf("columns do not stay visually aligned: %q", wide)
	}
}

func TestWideTableBecomesStackedRecords(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 30})
	got := o.Table([]string{"IDENTIFIER", "CREDENTIAL", "DECISION"}, [][]string{
		{"request-123456789", "hello-world", "approved"},
		{"request-987654321", "hello-tools", "denied"},
	})
	for _, want := range []string{"IDENTIFIER", "request-123456789", "CREDENTIAL", "hello-tools", "DECISION", "denied", "──"} {
		if !strings.Contains(got, want) {
			t.Errorf("stacked table lacks %q: %q", want, got)
		}
	}
}

func TestCalloutFitsAndRetainsDecisionFields(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 52})
	got := o.Callout(CalloutWarning, "Target confirmation", []Field{
		{Label: "context", Value: "kind-demo"},
		{Label: "posture", Value: "local kind"},
	})
	for _, want := range []string{"Target confirmation", "context", "kind-demo", "posture", "local kind", "╭", "╰"} {
		if !strings.Contains(got, want) {
			t.Errorf("callout lacks %q: %q", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 52 {
			t.Errorf("callout line is %d cells wide: %q", lipgloss.Width(line), line)
		}
	}
}

func TestLongCalloutFieldsWrapInsideTheBorder(t *testing.T) {
	o := WithCapabilities(Capabilities{Rich: true, Color: false, Width: 44})
	got := o.Callout(CalloutWarning, "Target confirmation", []Field{{
		Label: "server", Value: "a-very-long-managed-cluster-api-server.example.invalid",
	}})
	if !strings.Contains(got, "a-very-long") || !strings.Contains(got, "example.invalid") {
		t.Fatalf("callout lost a long field: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 44 {
			t.Errorf("callout line is %d cells wide: %q", lipgloss.Width(line), line)
		}
	}
}
