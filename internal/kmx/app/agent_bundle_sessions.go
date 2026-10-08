package app

import (
	"context"
	"fmt"
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
	cases, files, err := loadBundleEvaluationCases(bundle)
	if err != nil {
		return err
	}
	if opt.Case != "" {
		for i, c := range cases {
			if c.Case.ID == opt.Case {
				cases, files = cases[i:i+1], files[i:i+1]
				break
			}
		}
		if len(cases) != 1 || cases[0].Case.ID != opt.Case {
			return fmt.Errorf("no evaluation case with id %q in %s", opt.Case, agentruntime.EvaluationCaseDir)
		}
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.Sessions, CAFile: opt.SessionsCA})
	if err != nil {
		return err
	}
	defer client.Close()
	receipt := sessionsEvaluationReceipt{
		Bundle: portable.Metadata.Name, PortableDigest: digest,
		CasesDigest: agentruntime.EvaluationCasesDigest(files), FullCaseSet: opt.Case == "",
		GitCommit: liftAgentCommit(ctx, bundle, digest),
		Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{
			Version: 1, Provenance: "host-reported", Address: opt.Sessions,
		}},
	}
	ui := cliui.New(a.Out)
	counts := map[string]int{}
	var verdicts []bundleEvaluationResult
	for _, c := range cases {
		fmt.Fprintf(a.Out, "\n%s\n", ui.Heading("case "+c.Case.ID))
		caseCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := client.RunCase(caseCtx, agentsessions.CaseRequest{
			PortableDigest: digest, CasesDigest: receipt.CasesDigest,
			Instructions: portable.Spec.Instructions, Model: portable.Spec.Model.Name, Input: c.Case.Input,
		})
		cancel()
		entry := sessionsEvaluationResult{
			ID: c.Case.ID, Verdict: "unknown", SessionUID: result.SessionUID,
			Harness: result.Harness, Model: result.Model, ModelMixed: result.ModelMixed,
			JournalHead: sessionsJournalHead{Seq: result.Head.Seq, Hash: result.Head.Hash},
		}
		if result.Output != "" {
			entry.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(result.Output)
		}
		if err != nil {
			// The adapter only returns fixed diagnostics, never remote error bodies.
			entry.Detail = err.Error()
		} else {
			entry.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(result.Output)
			_, missing := agentruntime.MatchExpectations(result.Output, c.Case.ExpectContains)
			entry.Verdict = "pass"
			if len(missing) > 0 {
				entry.Verdict = "fail"
			}
			fmt.Fprintln(a.Out, result.Output)
		}
		line := fmt.Sprintf("%s: %s", entry.ID, entry.Verdict)
		if entry.Detail != "" {
			line += " — " + entry.Detail
		}
		fmt.Fprintln(a.Out, line)
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
	fmt.Fprintf(a.Out, "\nevaluation %s: %s (portable digest %s)\nreceipt: %s\n", receipt.Result, summary, shortSHA(digest), path)
	if receipt.Result != "pass" {
		return fmt.Errorf("evaluation did not pass: %s", summary)
	}
	return nil
}
