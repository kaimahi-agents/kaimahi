package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// File diagnostics span multiple cases, unlike the suite's per-case notes.
// These transient values never enter runtime evidence or retained receipts.
type fileJudgeKey struct{ caseID, assertionID string }
type fileJudgeNote struct {
	diagnostic suiteJudgeDiagnostic
	key        string
}
type fileJudgeNotes map[fileJudgeKey]fileJudgeNote
type fileJudgeContextKey struct{}

func newFileJudgeContext(ctx context.Context) (context.Context, fileJudgeNotes) {
	notes := fileJudgeNotes{}
	return context.WithValue(ctx, fileJudgeContextKey{}, notes), notes
}

func recordFileJudgeDiagnostic(ctx context.Context, note suiteJudgeDiagnostic, key string) {
	if notes, ok := ctx.Value(fileJudgeContextKey{}).(fileJudgeNotes); ok {
		notes[fileJudgeKey{note.CaseID, note.AssertionID}] = fileJudgeNote{note, key}
	}
}

// FilePreviewJudge is opt-in display content, never evidence or an average.
type FilePreviewJudge struct {
	Scores   []int    `json:"scores"`
	MinScore int      `json:"minScore"`
	Reasons  []string `json:"reasons"`
}

func previewJudge(row runtime.EvaluationAssertionResult, want runtime.EvaluationAssertion, note fileJudgeNote, reasonBytes *int) *FilePreviewJudge {
	e := row.Judge
	if want.Type != "llmJudge" || want.MinScore == nil || !runtime.EvaluationJudgeEvidenceValid(row) || e == nil || len(e.Scores) != 2 ||
		e.Model != want.JudgeModel || e.ModelVersion != want.JudgeVersion || e.MinScore != *want.MinScore {
		return nil
	}
	display := &FilePreviewJudge{Scores: slices.Clone(e.Scores), MinScore: e.MinScore, Reasons: []string{}}
	for _, reason := range note.diagnostic.Reasons {
		if len(display.Reasons) == 2 {
			break
		}
		if safe, ok := suiteDiagnosticText(reason, 4096, []string{note.key}); ok && safe != "" {
			encoded, _ := json.Marshal(safe)
			if len(encoded) > *reasonBytes {
				continue
			}
			*reasonBytes -= len(encoded)
			display.Reasons = append(display.Reasons, safe)
		}
	}
	return display
}
