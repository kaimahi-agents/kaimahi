package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

type suiteFailureReportingKey struct{}
type suiteFailureReporter struct {
	details *[]suiteFailureDetail
	out     io.Writer
	content bool
	key     string
}

func withSuiteFailureReporting(ctx context.Context, details *[]suiteFailureDetail, out io.Writer, content bool, keys ...string) context.Context {
	if out == nil {
		out = io.Discard
	}
	reporter := suiteFailureReporter{details: details, out: out, content: content}
	if len(keys) > 0 {
		reporter.key = keys[0]
	}
	return context.WithValue(ctx, suiteFailureReportingKey{}, reporter)
}
func reportSuiteFailure(ctx context.Context, detail *suiteFailureDetail) error {
	reporter, ok := ctx.Value(suiteFailureReportingKey{}).(suiteFailureReporter)
	if !ok || detail == nil {
		return nil
	}
	*reporter.details = append(*reporter.details, *detail)
	return writeSuiteFailureDetail(reporter.out, *detail, reporter.content)
}

// summary is a payload-free projection, never a passing evidence row. Content
// opt-in affects terminal diagnostics only, not evaluation/Lift outcome JSON.
func (d suiteFailureDetail) summary() suiteFailureDetail {
	d.Answer = ""
	d.Assertions = append([]suiteFailureAssertion(nil), d.Assertions...)
	for i := range d.Assertions {
		a := &d.Assertions[i]
		a.Expected = ""
		a.Reasons = nil
		a.Excerpt = ""
		a.ExcerptKind = ""
	}
	return d
}
func suiteFailureSummaries(details []suiteFailureDetail) []suiteFailureDetail {
	if len(details) == 0 {
		return nil
	}
	out := make([]suiteFailureDetail, len(details))
	for i, d := range details {
		out[i] = d.summary()
	}
	return out
}
func writeSuiteFailureDetail(out io.Writer, d suiteFailureDetail, content bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s run %d/%d: %s", d.CaseID, d.Run, d.Runs, d.Verdict)
	if d.Class != "" {
		fmt.Fprintf(&b, " [%s]", d.Class)
	}
	if len(d.Assertions) == 0 {
		b.WriteString("; assertions not evaluated")
	}
	b.WriteByte('\n')
	for _, a := range d.Assertions {
		fmt.Fprintf(&b, "    %s (%s): %s", a.ID, a.Type, a.Verdict)
		if a.Threshold != nil {
			if len(a.Scores) == 2 {
				fmt.Fprintf(&b, "; judge %d,%d / %d", a.Scores[0], a.Scores[1], *a.Threshold)
			} else {
				fmt.Fprintf(&b, "; judge unavailable / %d", *a.Threshold)
			}
		}
		if a.Class != "" {
			fmt.Fprintf(&b, " [%s]", a.Class)
		}
		b.WriteByte('\n')
		if content {
			if a.Expected != "" {
				fmt.Fprintf(&b, "      expected: %q\n", a.Expected)
			}
			if a.RationaleUnavailable {
				fmt.Fprintln(&b, "      rationale: unavailable or withheld")
			}
			for _, reason := range a.Reasons {
				fmt.Fprintf(&b, "      rationale: %q\n", reason)
			}
			if a.Excerpt != "" {
				fmt.Fprintf(&b, "      answer (%s excerpt): %q\n", a.ExcerptKind, a.Excerpt)
			}
		}
	}
	if content && d.AnswerStatus != "complete" {
		fmt.Fprintf(&b, "    answer: %s\n", d.AnswerStatus)
	}
	// Recheck the assembled output; model text never becomes a format string.
	if secretshapes.Match(b.String()) != nil {
		_, err := io.WriteString(out, "  failure detail withheld by credential-shape checks\n")
		return err
	}
	_, err := io.WriteString(out, b.String())
	return err
}
