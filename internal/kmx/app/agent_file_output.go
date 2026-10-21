package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteFilePreview renders the shared local-preview projection, not evidence.
func WriteFilePreview(w io.Writer, r FilePreviewResult, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(r)
	}
	if format != "text" {
		return &FilePreviewError{"invalid_options", 1}
	}
	var b strings.Builder
	scope := "full"
	if r.Selection.Scope == "subset" {
		scope = "partial"
	}
	fmt.Fprintf(&b, "kmx local preview of this charter (%s): %s\nrevision: %s\nmodel: %s/%s\n%s %s: %s\n", scope, r.File.Path, r.File.Revision, r.ModelRoute.Provider, r.ModelRoute.Model, r.State, r.Verdict, r.Reason)
	if r.Diagnostic != nil {
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", r.Diagnostic.Field, r.Diagnostic.Line, r.Diagnostic.Column, r.Diagnostic.Repair)
	}
	if r.JudgeRoute != nil {
		fmt.Fprintf(&b, "explicit judge: %s/%s\n", r.JudgeRoute.Provider, r.JudgeRoute.Model)
	}
	if r.AdHoc != nil {
		fmt.Fprintf(&b, "ad-hoc: %s\n", r.AdHoc.State)
		if r.AdHoc.AnswerExcerpt != "" {
			fmt.Fprintf(&b, "  answer excerpt: %q\n", r.AdHoc.AnswerExcerpt)
		}
	}
	for _, ex := range r.Examples {
		fmt.Fprintf(&b, "example %s: %s %s\n", ex.ID, ex.State, ex.Verdict)
		for _, as := range ex.Assertions {
			fmt.Fprintf(&b, "  %s %s %s: %s\n", as.ID, as.Type, as.Verdict, as.Reason)
		}
		if ex.AnswerExcerpt != "" {
			fmt.Fprintf(&b, "  answer excerpt: %q\n", ex.AnswerExcerpt)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
