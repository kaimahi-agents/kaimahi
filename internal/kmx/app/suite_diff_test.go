package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

func TestRenderSuiteDiffUnified(t *testing.T) {
	report := suiteDiffRenderFixture()
	var out bytes.Buffer
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: 80})
	if err := renderSuiteDiff(&out, ui, report, SuiteDiffUnified); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, want := range []string{
		"Summary: 0 added, 0 removed, 1 modified; 1 field change; 1 affected agent",
		"@@ ~ agent writer @@",
		`- description: "Old description"`,
		`+ description: "New description"`,
		"Affected agents",
		"agent definition modified",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("unified output does not contain %q:\n%s", want, rendered)
		}
	}
}

func TestRenderSuiteDiffSplit(t *testing.T) {
	report := suiteDiffRenderFixture()
	var out bytes.Buffer
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: true, Width: 72})
	if err := renderSuiteDiff(&out, ui, report, SuiteDiffSplit); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	plain := ansi.Strip(rendered)
	for _, want := range []string{"--- BEFORE", "+++ AFTER", `- description: "Old`, `+ description: "New`} {
		if !strings.Contains(plain, want) {
			t.Errorf("split output does not contain %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(plain, "\n"), "\n") {
		if strings.Contains(line, " | ") && ansi.StringWidth(line) > 72 {
			t.Errorf("split line is %d cells wide: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderSuiteDiffSanitizesTerminalControls(t *testing.T) {
	report := suiteDiffRenderFixture()
	report.Before.Suite = "before\u009b31m\nNo changes."
	report.Changes[0].ID = "writer\u009b32m\nSummary: forged\tline"
	report.Changes[0].Fields[0].Path = "description\u009b33m"
	report.Changes[0].Fields[0].Before = []byte("\"old\u009b34m\"")
	report.Changes[0].Fields[0].After = []byte("\"new\x1b[2J\"")
	report.AffectedAgents[0].ID = "writer\u009b35m"
	report.AffectedAgents[0].Reasons = []string{"agent\u009b36m definition\u202e modified\u2066"}

	for _, view := range []string{SuiteDiffUnified, SuiteDiffSplit} {
		t.Run(view, func(t *testing.T) {
			var out bytes.Buffer
			ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: 72})
			if err := renderSuiteDiff(&out, ui, report, view); err != nil {
				t.Fatal(err)
			}
			rendered := out.String()
			if strings.ContainsRune(rendered, '\u009b') || strings.ContainsRune(rendered, '\x1b') ||
				strings.ContainsRune(rendered, '\u202e') || strings.ContainsRune(rendered, '\u2066') {
				t.Fatalf("%s output contains terminal controls: %q", view, rendered)
			}
			for _, line := range strings.Split(rendered, "\n") {
				if line == "No changes." || line == "Summary: forged\tline" {
					t.Fatalf("%s output contains injected report line: %q", view, rendered)
				}
			}
		})
	}
}

func TestRenderSplitRecordFitsNarrowTerminal(t *testing.T) {
	var out bytes.Buffer
	const width = 20
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: width})
	if err := renderSplitRecord(&out, ui, suiteDiffRenderFixture().Changes[0]); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if strings.Contains(rendered, " | ") {
		t.Fatalf("narrow output used split columns:\n%s", rendered)
	}
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n") {
		if ansi.StringWidth(line) > width {
			t.Errorf("narrow line is %d cells wide: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderSuiteDiffSplitFitsEveryMeasuredLine(t *testing.T) {
	report := suiteDiffRenderFixture()
	report.Before.Suite = strings.Repeat("before", 10)
	report.After.Suite = strings.Repeat("after", 10)
	report.Changes[0].ID = strings.Repeat("writer", 10)
	report.AffectedAgents[0].ID = strings.Repeat("agent", 10)
	report.AffectedAgents[0].Reasons = []string{strings.Repeat("long reason ", 10)}
	var out bytes.Buffer
	const width = 24
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: width})
	if err := renderSuiteDiff(&out, ui, report, SuiteDiffSplit); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if ansi.StringWidth(line) > width {
			t.Errorf("line is %d cells wide: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderSuiteDiffSplitDoesNotGuessUnknownWidth(t *testing.T) {
	report := suiteDiffRenderFixture()
	report.Changes[0].Fields[0].Before = []byte(`"` + strings.Repeat("old", 50) + `"`)
	var out bytes.Buffer
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: false, Color: false, Width: 0})
	if err := renderSuiteDiff(&out, ui, report, SuiteDiffSplit); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "--- BEFORE | +++ AFTER") {
		t.Fatalf("unknown-width output omitted unbounded split columns:\n%s", rendered)
	}
	if !strings.Contains(rendered, `- description: "`+strings.Repeat("old", 50)+`"`) {
		t.Fatalf("unknown-width output wrapped the field:\n%s", rendered)
	}
}

func TestRenderSuiteDiffNoChanges(t *testing.T) {
	report := agentsuite.DiffReport{
		SchemaVersion:  agentsuite.DiffReportSchemaVersion,
		Before:         agentsuite.DiffIdentity{Suite: "same"},
		After:          agentsuite.DiffIdentity{Suite: "same"},
		Changes:        []agentsuite.RecordChange{},
		AffectedAgents: []agentsuite.AffectedAgent{},
	}
	var out bytes.Buffer
	if err := renderSuiteDiff(&out, cliui.WithCapabilities(cliui.Capabilities{}), report, SuiteDiffUnified); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No changes.") {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	const width = 5
	ui := cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: width})
	if err := renderSuiteDiff(&out, ui, report, SuiteDiffSplit); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if ansi.StringWidth(line) > width {
			t.Errorf("unchanged line is %d cells wide: %q", ansi.StringWidth(line), line)
		}
	}
}

func suiteDiffRenderFixture() agentsuite.DiffReport {
	return agentsuite.DiffReport{
		SchemaVersion: agentsuite.DiffReportSchemaVersion,
		Before:        agentsuite.DiffIdentity{Suite: "before"},
		After:         agentsuite.DiffIdentity{Suite: "after"},
		Summary: agentsuite.DiffSummary{
			Modified:       1,
			FieldsChanged:  1,
			AgentsAffected: 1,
		},
		Changes: []agentsuite.RecordChange{{
			Type:         agentsuite.DiffModified,
			Kind:         "agent",
			ID:           "writer",
			BeforeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AfterDigest:  "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Fields: []agentsuite.FieldChange{{
				Path:   "description",
				Before: []byte(`"Old description"`),
				After:  []byte(`"New description"`),
			}},
		}},
		AffectedAgents: []agentsuite.AffectedAgent{{
			ID:      "writer",
			Reasons: []string{"agent definition modified"},
		}},
	}
}
