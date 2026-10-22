package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	suiteoci "github.com/agent-primitives/suite/oci"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

const (
	SuiteDiffUnified = "unified"
	SuiteDiffSplit   = "split"
)

// DiffSuites compares two conformant AgentSuites without contacting a registry.
func (a *App) DiffSuites(beforePath, afterPath, output, view string) error {
	switch output {
	case "json":
	case "text":
		if view != SuiteDiffUnified && view != SuiteDiffSplit {
			return fmt.Errorf("unsupported diff view %q; use unified or split", view)
		}
	default:
		return fmt.Errorf("unsupported output %q; use text or json", output)
	}
	report, err := suiteoci.DiffPaths(beforePath, afterPath)
	if err != nil {
		return err
	}
	switch output {
	case "json":
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "text":
		return renderSuiteDiff(a.Out, cliui.New(a.Out), report, view)
	}
	return nil
}

func renderSuiteDiff(out io.Writer, ui cliui.Output, report suiteoci.DiffReport, view string) error {
	if view != SuiteDiffUnified && view != SuiteDiffSplit {
		return fmt.Errorf("unsupported diff view %q; use unified or split", view)
	}
	width := 0
	if view == SuiteDiffSplit {
		width = ui.Width()
	}
	if err := writeSuiteDiffLine(out, ui.Heading, fmt.Sprintf("AgentSuite diff: %s -> %s", report.Before.Suite, report.After.Suite), width); err != nil {
		return err
	}
	summary := report.Summary
	summaryLine := fmt.Sprintf(
		"Summary: %d added, %d removed, %d modified; %d field change%s; %d affected agent%s\n",
		summary.Added, summary.Removed, summary.Modified,
		summary.FieldsChanged, pluralSuffix(summary.FieldsChanged),
		summary.AgentsAffected, pluralSuffix(summary.AgentsAffected),
	)
	if err := writeSuiteDiffLine(out, identitySuiteDiffStyle, strings.TrimSuffix(summaryLine, "\n"), width); err != nil {
		return err
	}
	if !report.HasChanges() {
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		return writeSuiteDiffLine(out, identitySuiteDiffStyle, "No changes.", width)
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if err := writeSuiteDiffLine(out, ui.Heading, "Changes", width); err != nil {
		return err
	}
	for _, change := range report.Changes {
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		if err := writeSuiteDiffLine(out, ui.Info, diffRecordHeader(change), width); err != nil {
			return err
		}
		switch view {
		case SuiteDiffUnified:
			if err := renderUnifiedRecord(out, ui, change); err != nil {
				return err
			}
		case SuiteDiffSplit:
			if err := renderSplitRecord(out, ui, change); err != nil {
				return err
			}
		}
	}
	if len(report.AffectedAgents) > 0 {
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
		if err := writeSuiteDiffLine(out, ui.Heading, "Affected agents", width); err != nil {
			return err
		}
		for _, agent := range report.AffectedAgents {
			if err := writeSuiteDiffLine(out, ui.Accent, "  "+agent.ID, width); err != nil {
				return err
			}
			for _, reason := range agent.Reasons {
				if err := writeSuiteDiffLine(out, identitySuiteDiffStyle, "    - "+reason, width); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func renderUnifiedRecord(out io.Writer, ui cliui.Output, change suiteoci.RecordChange) error {
	for _, field := range change.Fields {
		if field.Before != nil {
			if _, err := fmt.Fprintln(out, ui.Failure(safeSuiteDiffTerminal("- "+field.Path+": "+string(field.Before)))); err != nil {
				return err
			}
		}
		if field.After != nil {
			if _, err := fmt.Fprintln(out, ui.Success(safeSuiteDiffTerminal("+ "+field.Path+": "+string(field.After)))); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderSplitRecord(out io.Writer, ui cliui.Output, change suiteoci.RecordChange) error {
	width := ui.Width()
	if width == 0 {
		return renderUnboundedSplitRecord(out, ui, change)
	}
	if width < 25 {
		return renderNarrowDiffRecord(out, ui, change, width)
	}
	cellWidth := (width - 3) / 2
	if _, err := fmt.Fprintf(out, "%s | %s\n",
		padDiffCell("--- BEFORE", cellWidth),
		padDiffCell("+++ AFTER", cellWidth),
	); err != nil {
		return err
	}
	for _, field := range change.Fields {
		var before, after string
		if field.Before != nil {
			before = safeSuiteDiffTerminal("- " + field.Path + ": " + string(field.Before))
		}
		if field.After != nil {
			after = safeSuiteDiffTerminal("+ " + field.Path + ": " + string(field.After))
		}
		beforeLines := wrapDiffCell(before, cellWidth)
		afterLines := wrapDiffCell(after, cellWidth)
		rows := max(len(beforeLines), len(afterLines))
		for index := range rows {
			var left, right string
			if index < len(beforeLines) {
				left = beforeLines[index]
			}
			if index < len(afterLines) {
				right = afterLines[index]
			}
			left = padDiffCell(left, cellWidth)
			right = padDiffCell(right, cellWidth)
			if before != "" {
				left = ui.Failure(left)
			}
			if after != "" {
				right = ui.Success(right)
			}
			if _, err := fmt.Fprintf(out, "%s | %s\n", left, right); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderUnboundedSplitRecord(out io.Writer, ui cliui.Output, change suiteoci.RecordChange) error {
	if _, err := fmt.Fprintln(out, "--- BEFORE | +++ AFTER"); err != nil {
		return err
	}
	for _, field := range change.Fields {
		var before, after string
		if field.Before != nil {
			before = safeSuiteDiffTerminal("- " + field.Path + ": " + string(field.Before))
		}
		if field.After != nil {
			after = safeSuiteDiffTerminal("+ " + field.Path + ": " + string(field.After))
		}
		if before != "" {
			before = ui.Failure(before)
		}
		if after != "" {
			after = ui.Success(after)
		}
		if _, err := fmt.Fprintf(out, "%s | %s\n", before, after); err != nil {
			return err
		}
	}
	return nil
}

func renderNarrowDiffRecord(out io.Writer, ui cliui.Output, change suiteoci.RecordChange, width int) error {
	width = max(width, 1)
	for _, field := range change.Fields {
		if field.Before != nil {
			value := safeSuiteDiffTerminal("- " + field.Path + ": " + string(field.Before))
			for _, line := range strings.Split(ansi.Hardwrap(value, width, true), "\n") {
				if _, err := fmt.Fprintln(out, ui.Failure(line)); err != nil {
					return err
				}
			}
		}
		if field.After != nil {
			value := safeSuiteDiffTerminal("+ " + field.Path + ": " + string(field.After))
			for _, line := range strings.Split(ansi.Hardwrap(value, width, true), "\n") {
				if _, err := fmt.Fprintln(out, ui.Success(line)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeSuiteDiffLine(out io.Writer, style func(string) string, value string, width int) error {
	value = safeSuiteDiffTerminal(value)
	lines := []string{value}
	if width > 0 {
		lines = strings.Split(ansi.Hardwrap(value, width, true), "\n")
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, style(line)); err != nil {
			return err
		}
	}
	return nil
}

func identitySuiteDiffStyle(value string) string {
	return value
}

func safeSuiteDiffTerminal(value string) string {
	value = safeTerminal(value)
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func wrapDiffCell(value string, width int) []string {
	if value == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(value, width, ""), "\n")
}

func padDiffCell(value string, width int) string {
	value = ansi.Truncate(value, width, "...")
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

func diffRecordHeader(change suiteoci.RecordChange) string {
	marker := "~"
	switch change.Type {
	case suiteoci.DiffAdded:
		marker = "+"
	case suiteoci.DiffRemoved:
		marker = "-"
	}
	header := fmt.Sprintf("@@ %s %s %s @@", marker, change.Kind, change.ID)
	switch {
	case change.BeforeDigest != "" && change.AfterDigest != "":
		header += "  " + shortSuiteDigest(change.BeforeDigest) + " -> " + shortSuiteDigest(change.AfterDigest)
	case change.BeforeDigest != "":
		header += "  " + shortSuiteDigest(change.BeforeDigest)
	case change.AfterDigest != "":
		header += "  " + shortSuiteDigest(change.AfterDigest)
	}
	return header
}

func shortSuiteDigest(value string) string {
	const length = len("sha256:") + 12
	if len(value) <= length {
		return value
	}
	return value[:length] + "..."
}
