package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// WriteFilePreview renders the shared local-preview projection, not evidence.
func WriteFilePreview(w io.Writer, r FilePreviewResult, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(r)
	}
	if format != "text" {
		return &FilePreviewError{code: "invalid_options", exit: 1}
	}
	return writeFilePreviewText(w, r, true)
}

// WriteFilePreviewDetails shares file text details without a standalone report heading.
func WriteFilePreviewDetails(w io.Writer, r FilePreviewResult) error {
	return writeFilePreviewText(w, r, false)
}

func writeFilePreviewText(w io.Writer, r FilePreviewResult, standalone bool) error {
	var b strings.Builder
	if r.Runner != "" {
		fmt.Fprintln(&b, r.Runner)
	}
	scope := "full"
	if r.Selection.Scope == "subset" {
		scope = "partial"
	}
	if standalone {
		fmt.Fprintf(&b, "kmx local preview of this charter (%s): %s\n", scope, r.File.Path)
	}
	fmt.Fprintf(&b, "revision: %s\nmodel: %s/%s\n%s %s: %s\n", r.File.Revision, r.ModelRoute.Provider, r.ModelRoute.Model, r.State, r.Verdict, r.Reason)
	if r.Tests != nil {
		fmt.Fprintf(&b, "tests: %s (%s)\ntests revision: %s\nsnapshot revision: %s\n", r.Tests.Path, r.Tests.State, r.Tests.Revision, r.SnapshotRevision)
	}
	for _, notice := range r.Notices {
		fmt.Fprintln(&b, notice)
	}
	if r.SavedRevision != "" {
		if r.RecordingTarget != nil {
			fmt.Fprintf(&b, "saved %s revision: %s; saved snapshot: %s (results use the executed snapshot above)\n", r.RecordingTarget.Role, r.SavedRevision, r.SavedSnapshotRevision)
		} else {
			fmt.Fprintf(&b, "saved revision: %s (results use the executed revision above)\n", r.SavedRevision)
		}
	}
	for _, tool := range r.ToolSettings {
		fmt.Fprintf(&b, "tool setting %s: %s\n", tool.Name, tool.State)
	}
	if r.Diagnostic != nil {
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", r.Diagnostic.Field, r.Diagnostic.Line, r.Diagnostic.Column, r.Diagnostic.Repair)
	}
	if r.JudgeRoute != nil {
		fmt.Fprintf(&b, "explicit judge: %s/%s\n", r.JudgeRoute.Provider, r.JudgeRoute.Model)
	}
	if r.Retention.Status != "not_requested" {
		fmt.Fprintf(&b, "failure retention: %s\n", r.Retention.Status)
		for _, diagnostic := range r.Retention.Diagnostics {
			fmt.Fprintf(&b, "  example %s: diagnostic %s\n", diagnostic.CaseID, diagnostic.ID)
		}
		if len(r.Retention.Diagnostics) > 0 {
			fmt.Fprintln(&b, "Inspect retained failures with kmx test explain --file FILE [failure-id]; add --show-content for bounded content.")
		}
	}
	if r.AdHoc != nil {
		fmt.Fprintf(&b, "ad-hoc: %s\n", r.AdHoc.State)
		writeFileTools(&b, *r.AdHoc, r.ToolSettings)
		if r.AdHoc.AnswerExcerpt != "" {
			fmt.Fprintf(&b, "  answer excerpt: %q\n", r.AdHoc.AnswerExcerpt)
		}
	}
	for _, ex := range r.Examples {
		label := strings.ToUpper(string(ex.Verdict))
		if ex.State != "complete" {
			label = strings.ToUpper(ex.State)
		} else if r.Operation == "run" && ex.ReasonCode == "completion_ready" && len(ex.Assertions) == 0 {
			label = "ANSWERED"
		}
		fmt.Fprintf(&b, "%s %s (%dms)\n", label, ex.ID, ex.DurationMs)
		writeFileTools(&b, ex.FilePreviewCompletion, r.ToolSettings)
		for _, as := range ex.Assertions {
			reason := as.Reason
			if as.Failure != "" {
				reason = as.Failure
			}
			fmt.Fprintf(&b, "  %s %s %s: %s\n", as.ID, as.Type, as.Verdict, reason)
		}
		if ex.AnswerExcerpt != "" {
			lines := strings.Split(ex.AnswerExcerpt, "\n")
			if len(lines) > 5 {
				lines = lines[:5]
			}
			fmt.Fprintf(&b, "  answer:\n    %s\n", strings.Join(lines, "\n    "))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteFileRunAnswers keeps completed text Run stdout usable as an answer pipe.
// Ad-hoc input selects its answer; otherwise each saved example answer is emitted.
func WriteFileRunAnswers(w io.Writer, r FilePreviewResult) error {
	write := func(c FilePreviewCompletion) error {
		if c.Answer == nil {
			return nil
		}
		answer := *c.Answer
		if _, err := io.WriteString(w, answer); err != nil {
			return err
		}
		if answer != "" && !strings.HasSuffix(answer, "\n") {
			_, err := io.WriteString(w, "\n")
			return err
		}
		return nil
	}
	if r.AdHoc != nil {
		return write(*r.AdHoc)
	}
	for _, row := range r.Examples {
		if err := write(row.FilePreviewCompletion); err != nil {
			return err
		}
	}
	return nil
}

func fileToolUnavailable(tool agentpreview.ToolSummary, settings []localtools.ToolSetting) bool {
	return tool.Source == "unavailable" && !localtools.IsDisabled(settings, tool.Name)
}

func writeFileTools(b *strings.Builder, c FilePreviewCompletion, settings []localtools.ToolSetting) {
	for _, tool := range c.Tools {
		usage := "Not called"
		if tool.CallCount > 0 {
			usage = fmt.Sprintf("%d call(s)", tool.CallCount)
		}
		if tool.RecordingSaved {
			usage += "; recorded just now"
		}
		fmt.Fprintf(b, "  tool %s: %s; mode=%s source=%s\n", tool.Name, usage, tool.Mode, tool.Source)
		if fileToolUnavailable(tool, settings) {
			fmt.Fprintln(b, "    No recording; tool unavailable.")
		}
	}
}
