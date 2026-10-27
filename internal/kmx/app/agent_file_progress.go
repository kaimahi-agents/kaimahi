package app

import (
	"context"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// FilePreviewCaseProgress is provisional, content-free case metadata. A later
// journal/finalization failure can still veto the full run's verdict.
type FilePreviewCaseProgress struct {
	CaseID            string                    `json:"caseId"`
	Verdict           runtime.EvaluationVerdict `json:"verdict"`
	Reason            string                    `json:"reason"`
	FailedAssertionID string                    `json:"failedAssertionId,omitempty"`
}

func previewCaseObserver(ctx context.Context, selected []agentfile.Example, agent *agentfile.Agent, route modelaccess.Route, notify func(FilePreviewCaseProgress), skipAssertions bool) func(agentpreview.ExampleOutcome) {
	if notify == nil {
		return nil
	}
	next := 0
	seen := map[string]bool{}
	return func(row agentpreview.ExampleOutcome) {
		if ctx.Err() != nil || next >= len(selected) {
			return
		}
		ex := selected[next]
		c := row.Completion
		// Apply the final projection's attribution and assertion fences before any
		// metadata escapes. A malformed runner event disables further observation,
		// without changing the runner's eventual outcome or App's final validation.
		assertionsOK := len(row.Assertions) == len(ex.Assertions) && len(row.Assertions) > 0
		if skipAssertions {
			assertionsOK = len(row.Assertions) == 0
		}
		valid := validPreviewTools(c, ex, agent.Tools) && row.ID == ex.ID && c.State == agentpreview.Complete && c.ID != "" && !seen[c.ID] &&
			c.FileRevision == agent.Revision && c.Provider == route.Provider && c.Model == route.Model &&
			len(c.Answer) <= 256*1024 && utf8.ValidString(c.Answer) &&
			c.ToolCalls >= 0 && c.ToolCalls <= localtools.MaxCalls && (len(agent.Tools) > 0 || c.ToolCalls == 0) &&
			assertionsOK
		for j, as := range row.Assertions {
			if j >= len(ex.Assertions) {
				valid = false
				break
			}
			want := ex.Assertions[j]
			if as.ID != want.ID || as.Type != want.Type || as.DefinitionDigest != runtime.EvaluationAssertionDigest(want) || as.Verdict != runtime.EvaluationPass && as.Verdict != runtime.EvaluationFail && as.Verdict != runtime.EvaluationUnknown {
				valid = false
				break
			}
		}
		if !valid {
			next = len(selected)
			return
		}
		seen[c.ID] = true
		next++
		if skipAssertions {
			notify(FilePreviewCaseProgress{CaseID: ex.ID, Verdict: runtime.EvaluationUnknown, Reason: previewReason("completion_ready")})
			return
		}
		progress := FilePreviewCaseProgress{CaseID: ex.ID, Verdict: runtime.EvaluationAssertionsVerdict(row.Assertions)}
		for _, as := range row.Assertions {
			if progress.Verdict != runtime.EvaluationPass && as.Verdict == progress.Verdict {
				progress.Reason = previewAssertion(as.ID, as.Type, as.Verdict).Reason
				break
			}
		}
		// The canvas can explain this authored check without sending operands or
		// answers in progress. Match its final text-check policy (judges are separate).
		if progress.Verdict == runtime.EvaluationFail {
			for _, as := range row.Assertions {
				if as.Verdict == runtime.EvaluationFail && as.Type != "llmJudge" {
					progress.FailedAssertionID = as.ID
					break
				}
			}
		}
		notify(progress)
	}
}
