package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Unknown evidence takes precedence over a known assertion failure. A threshold
// counts completed observations, never missing results or unexecuted runs.
func suiteRunVerdict(rows []agentruntime.EvaluationAssertionResult) agentruntime.EvaluationVerdict {
	verdict := agentruntime.EvaluationPass
	if len(rows) == 0 {
		return agentruntime.EvaluationUnknown
	}
	for _, r := range rows {
		switch r.Verdict {
		case agentruntime.EvaluationFail:
			verdict = agentruntime.EvaluationFail
		case agentruntime.EvaluationPass:
		default:
			return agentruntime.EvaluationUnknown
		}
	}
	return verdict
}

func suiteRunComplete(observed suitePassingObservation, frame suitePassingInventoryCase) bool {
	return observed.Detail == "" && !observed.Stop && observed.AnswerSHA256 != "" && len(observed.Assertions) == len(frame.Metadata.Assertions)
}

// Execution identifiers, scores and model matches are observations, not authored identity.
// Canonical coherence also removes the transport configuration, which is bound
// by the strict evidence validator but not selected by offline source content.
func suitePassingDefinition(row suitePassingCase, dropConfiguration bool) suitePassingCase {
	row.TaskName, row.TaskUID, row.AnswerDigest = "", "", ""
	row.KagentExecution = nil
	row.SessionsExecution = nil
	row.Verdict = "pass"
	row.Assertions = append([]suitePassingAssertion(nil), row.Assertions...)
	for i, a := range row.Assertions {
		row.Assertions[i].Verdict = "pass"
		if a.Judge != nil {
			identity := *a.Judge
			identity.Scores, identity.ModelMatches = nil, nil
			if dropConfiguration {
				identity.ConfigurationDigest = ""
			}
			row.Assertions[i].Judge = &identity
		}
	}
	return row
}

// Rows remain ordered by logical case, then run ordinal. Only declared complete
// repeated groups may reuse a case ID/path; native execution IDs never repeat.
func validateSuiteRunGroups(rows []suitePassingCase, mode string) error {
	ids, paths := map[string]bool{}, map[string]bool{}
	for i := 0; i < len(rows); {
		first := rows[i]
		if ids[first.ID] || paths[first.Path] {
			return errSuitePassingReceipt
		}
		ids[first.ID], paths[first.Path] = true, true
		if first.Run == 0 {
			if first.Runs != 0 || first.MinPasses != 0 || first.Verdict != "pass" {
				return errSuitePassingReceipt
			}
			i++
			continue
		}
		if mode == "native-agentsessions" || first.Run != 1 || first.Runs < 2 || first.Runs > agentruntime.EvaluationMaxRuns || first.MinPasses < 1 || first.MinPasses > first.Runs || first.Runs > len(rows)-i {
			return errSuitePassingReceipt
		}
		passes := 0
		definition := suitePassingDefinition(first, false)
		for j := 0; j < first.Runs; j++ {
			row := rows[i+j]
			if row.Run != j+1 {
				return errSuitePassingReceipt
			}
			comparable := suitePassingDefinition(row, false)
			comparable.Run = 1
			if !reflect.DeepEqual(comparable, definition) {
				return errSuitePassingReceipt
			}
			switch row.Verdict {
			case "pass":
				passes++
			case "fail":
			default:
				return errSuitePassingReceipt
			}
		}
		if passes < first.MinPasses {
			return errSuitePassingReceipt
		}
		i += first.Runs
	}
	return nil
}

// Each fresh run gets the configured deadline, including its judging and
// attribution checks. A late unknown vetoes an already attainable threshold.
func evaluateSuiteRuns(ctx context.Context, frames []suitePassingInventoryCase, timeout time.Duration, out io.Writer, run func(context.Context, suitePassingInventoryCase) (suitePassingObservation, error)) ([]suitePassingObservation, error) {
	observations := make([]suitePassingObservation, 0, len(frames))
	for i := 0; i < len(frames); {
		count, required := frames[i].Case.RunPolicy()
		if count > len(frames)-i {
			return nil, errSuiteNativeEvaluation
		}
		passes := 0
		for j := 0; j < count; j++ {
			frame := frames[i+j]
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			observed, err := run(runCtx, frame)
			if cause := runCtx.Err(); cause != nil {
				// Cancellation invalidates a countable failure, but must retain
				// other classified errors and the ambiguous-outcome disclosure.
				if errors.Is(err, errSuiteNativeCaseFailed) {
					err = nil
				}
				err = errors.Join(err, errSuiteNativeEvaluation, cause)
			}
			// The Orka executor uses CaseFailed for both a failed Task and a failed
			// assertion. Only full attributed assertion evidence permits continuing.
			complete := suiteRunComplete(observed, frame)
			verdict := agentruntime.EvaluationUnknown
			if complete && (err == nil || errors.Is(err, errSuiteNativeCaseFailed)) {
				verdict = suiteRunVerdict(observed.Assertions)
			} else if errors.Is(err, errSuiteNativeCaseFailed) && observed.Verdict == agentruntime.EvaluationFail {
				verdict = agentruntime.EvaluationFail
			}
			label := frame.Case.ID
			if count > 1 {
				label = fmt.Sprintf("%s run %d/%d", label, j+1, count)
			}
			if _, outputErr := fmt.Fprintf(out, "%s: %s\n", label, verdict); outputErr != nil {
				cancel()
				return nil, outputErr
			}
			if err != nil && (!errors.Is(err, errSuiteNativeCaseFailed) || !complete) {
				cancel()
				return nil, err
			}
			if !complete {
				cancel()
				return nil, errSuiteNativeEvaluation
			}
			if verdict == agentruntime.EvaluationUnknown {
				cancel()
				return nil, errSuiteNativeEvaluation
			}
			if err != nil && verdict != agentruntime.EvaluationFail {
				cancel()
				return nil, errSuiteNativeEvaluation
			}
			observed.Verdict = verdict
			if verdict == agentruntime.EvaluationPass {
				passes++
			}
			observations = append(observations, observed)
			cancel()
		}
		if passes < required {
			return nil, errSuiteNativeCaseFailed
		}
		i += count
	}
	return observations, nil
}
