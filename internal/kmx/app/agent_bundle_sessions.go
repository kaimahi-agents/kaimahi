package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// evaluateAgentBundleSessions runs one explicitly selected chat session per
// case. It has no Kubernetes dependency and never changes the host's model.
func (a *App) evaluateAgentBundleSessions(opt EvaluateAgentBundleOptions) error {
	if a.Out == nil {
		return fmt.Errorf("evaluate requires an output stream")
	}
	timeout := opt.CaseTimeout
	if timeout == 0 {
		timeout = defaultEvaluationCaseTimeout
	}
	if timeout < 10*time.Second || timeout > maxEvaluationCaseTimeout {
		return fmt.Errorf("--case-timeout must be between 10s and %s", maxEvaluationCaseTimeout)
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bundle, err := resolveOrkaPath(opt.BundleDir)
	if err != nil {
		return fmt.Errorf("resolve bundle: %w", err)
	}
	portable, _, digest, err := readSessionsEvaluationSource(bundle)
	if err != nil {
		return err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return err
	}
	var cases []bundleEvaluationCase
	var files []agentruntime.EvaluationCaseFile
	var references []agentruntime.SessionReferenceCase
	if opt.FromSessions != "" {
		cases, files, references, err = loadSessionReferenceEvaluationCases(opt.FromSessions, opt.SourceSessions)
	} else {
		cases, files, err = loadBundleEvaluationCases(bundle)
	}
	if err != nil {
		return err
	}
	if opt.Case != "" {
		for i, c := range cases {
			if c.Case.ID == opt.Case {
				cases, files = cases[i:i+1], files[i:i+1]
				if references != nil {
					references = references[i : i+1]
				}
				break
			}
		}
		if len(cases) != 1 || cases[0].Case.ID != opt.Case {
			return fmt.Errorf("no evaluation case with id %q in %s", opt.Case, agentruntime.EvaluationCaseDir)
		}
	}
	judgeOut := a.Out
	if opt.FromSessions != "" && !opt.ShowContent {
		judgeOut = io.Discard
	}
	judge, err := newEvaluationJudge(evaluationJudgeConfig{BaseURL: opt.JudgeBaseURL, KeyEnv: opt.JudgeKeyEnv}, cases, judgeOut)
	if err != nil {
		return err
	}
	runID, err := randomHex(16)
	if err != nil {
		return err
	}
	casesDigest := agentruntime.EvaluationCasesDigest(files)
	gitCommit := liftAgentCommit(ctx, bundle, digest)
	if err := preflightEvaluationReceiptSize(cases, portable.Spec.Model.Name, portable.Metadata.Name, opt.Sessions, digest, casesDigest, gitCommit, runID, "2", "agentsessions", "1", "host-reported", "chat"); err != nil {
		return err
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.Sessions, CAFile: opt.SessionsCA})
	if err != nil {
		return err
	}
	defer client.Close()
	var source *agentsessions.Client
	if references != nil {
		source, err = agentsessions.Dial(agentsessions.Options{Address: opt.SourceSessions, CAFile: opt.SourceSessionsCA})
		if err != nil {
			return err
		}
		defer source.Close()
	}
	receipt := sessionsEvaluationReceipt{
		SchemaVersion: 2, RunID: runID,
		Bundle: portable.Metadata.Name, PortableDigest: digest,
		CasesDigest: casesDigest, FullCaseSet: opt.Case == "",
		GitCommit: gitCommit,
		Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{
			Version: 1, Provenance: "host-reported", Address: opt.Sessions,
		}},
	}
	ui := cliui.New(a.Out)
	counts := map[string]int{}
	var verdicts []bundleEvaluationResult
	reportUnavailable := false
	for i, c := range cases {
		if _, err := fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("case "+c.Case.ID)); err != nil && source != nil {
			reportUnavailable = true
		}
		caseCtx, cancel := context.WithTimeout(ctx, timeout)
		var result agentsessions.CaseResult
		inputDigest := agentruntime.EvaluationInputDigest(c.Case.Input)
		var runErr error
		if source != nil {
			inputDigest = references[i].Source.InputDigest
			c.Case.Input, runErr = source.ResolveSessionReference(caseCtx, references[i].Source)
		}
		if runErr == nil {
			result, runErr = client.RunCase(caseCtx, agentsessions.CaseRequest{
				PortableDigest: digest, CasesDigest: receipt.CasesDigest,
				Instructions: portable.Spec.Instructions, Model: portable.Spec.Model.Name, Input: c.Case.Input,
			})
		}
		entry := sessionsEvaluationResult{
			CaseDigest: agentruntime.EvaluationCaseDigest(files[i].Bytes), InputDigest: inputDigest,
			ID: c.Case.ID, Verdict: "unknown", SessionUID: result.SessionUID,
			Harness: result.Harness, Model: result.Model, ModelMixed: result.ModelMixed,
			JournalHead: sessionsJournalHead{Seq: result.Head.Seq, Hash: result.Head.Hash},
		}
		if result.Output != "" {
			entry.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(result.Output)
		}
		entry.Assertions = evaluateCaseWithJudge(caseCtx, c.Case, agentruntime.EvaluationEvidence{ToolSupported: false}, judge)
		if runErr != nil {
			entry.Detail = evaluationExecutionUnavailable
			if result.ExecutionFailed {
				entry.Verdict = "fail"
				entry.Detail = evaluationExecutionFailed
			}
		} else {
			entry.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(result.Output)
			var printErr error
			if source == nil || opt.ShowContent {
				_, printErr = fmt.Fprintln(a.Out, result.Output)
			}
			if printErr != nil {
				entry.Detail = evaluationResultUnavailable
			} else {
				entry.Assertions = evaluateCaseWithJudge(caseCtx, c.Case, agentruntime.EvaluationEvidence{Answer: result.Output, AnswerAvailable: true, ToolSupported: false, ToolCalls: result.ToolCalls, ToolEvidenceComplete: result.ToolEvidenceComplete}, judge)
				entry.Verdict = string(agentruntime.EvaluationAssertionsVerdict(entry.Assertions))
			}
		}
		cancel()
		line := fmt.Sprintf("%s: %s", entry.ID, entry.Verdict)
		if entry.Detail != "" {
			line += " — " + entry.Detail
		}
		if source != nil {
			line += fmt.Sprintf(" (source %s prefix %s input %s; session %s answer %s)", references[i].Source.SessionUID, references[i].Source.PrefixHash, entry.InputDigest, entry.SessionUID, entry.AnswerSHA256)
		}
		if _, err := fmt.Fprintln(a.Out, line); err != nil && source != nil {
			reportUnavailable = true
		}
		receipt.Cases = append(receipt.Cases, entry)
		verdicts = append(verdicts, bundleEvaluationResult{Verdict: entry.Verdict})
		counts[entry.Verdict]++
	}
	// Only name a common host-reported harness/model when every case observed it.
	receipt.Target.Identity.Harness = receipt.Cases[0].Harness
	receipt.Target.Identity.Model = receipt.Cases[0].Model
	for _, c := range receipt.Cases[1:] {
		if c.Harness != receipt.Target.Identity.Harness {
			receipt.Target.Identity.Harness = ""
		}
		if c.Model != receipt.Target.Identity.Model {
			receipt.Target.Identity.Model = ""
		}
	}
	receipt.Result = bundleEvaluationOverall(verdicts)
	path, err := writeSessionsEvaluationReceipt(bundle, receipt)
	if err != nil {
		return fmt.Errorf("evaluation finished but its receipt was not saved: %w", err)
	}
	summary := fmt.Sprintf("%d passed, %d failed, %d unknown", counts["pass"], counts["fail"], counts["unknown"])
	if source != nil {
		if _, err := fmt.Fprintf(a.Out, "\nevaluation %s: %s (portable digest %s; cases digest %s; run %s)\n", receipt.Result, summary, digest, receipt.CasesDigest, receipt.RunID); err != nil {
			reportUnavailable = true
		}
	} else {
		fmt.Fprintf(a.Out, "\nevaluation %s: %s (portable digest %s)\nhistory: %s\nreceipt: %s\n", receipt.Result, summary, shortSHA(digest), evaluationHistoryPath(bundle, receipt.RunID), path)
	}
	if reportUnavailable {
		return fmt.Errorf("evaluation receipts saved but report output unavailable")
	}
	if receipt.Result != "pass" {
		return fmt.Errorf("evaluation did not pass: %s", summary)
	}
	return nil
}
