package app

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// These private options deliberately carry no portable revision or runtime
// override. The complete placement supplies the owned Agent and live subject.
type suiteNativeEvaluationOptions struct {
	ResultPort  string
	Judge       evaluationJudgeConfig
	ShowContent bool
}

// Case identities accompany payload-free native evidence for later run
// orchestration. This is not a persisted receipt or a public runtime API.
type suiteNativeCaseResult struct {
	agentruntime.EvaluationReceipt
	CaseDigest  string
	InputDigest string
}

// evaluateSuiteNative accepts only a verified workspace.Load case and the
// guarded placement for that same packaged suite. It never reads authored
// source or renders a new candidate. The caller must preserve this association,
// confirm Task execution before placement, and verifySuiteNative once more at
// final run completion; per-case checks cannot establish a final run subject.
func (a *App) evaluateSuiteNative(ctx context.Context, placement *suiteNativePlacement, packaged workspace.PackagedCase, opt suiteNativeEvaluationOptions) (suiteNativeCaseResult, error) {
	result := suiteNativeCaseResult{EvaluationReceipt: agentruntime.EvaluationReceipt{Verdict: agentruntime.EvaluationUnknown, Detail: evaluationExecutionUnavailable}}
	unavailable := func() (suiteNativeCaseResult, error) {
		// Parsers, preflight and runtime diagnostics can contain content or
		// credentials. No raw wrapped error leaves this new summary boundary.
		return result, fmt.Errorf("native suite case unavailable; subject or execution could not be verified")
	}
	caseDigest := agentruntime.EvaluationCaseDigest(packaged.Bytes)
	if packaged.Digest != "sha256:"+caseDigest {
		return unavailable()
	}
	c, err := agentruntime.ParseEvaluationCase(packaged.Bytes)
	if err != nil {
		return unavailable()
	}
	result.CaseDigest, result.InputDigest, result.CaseID = caseDigest, agentruntime.EvaluationInputDigest(c.Input), c.ID
	var content io.Writer = io.Discard
	if opt.ShowContent {
		content = a.Out
	}
	judge, err := newEvaluationJudge(opt.Judge, []bundleEvaluationCase{{Case: c}}, content)
	if err != nil {
		return unavailable()
	}
	result.Assertions = evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{ToolSupported: false}, judge)
	if a.Cfg == nil || a.Run == nil || !a.guarded || placement == nil || !placement.Complete || placement.plan == nil || len(placement.Resources) != 2 {
		return unavailable()
	}
	if ctx == nil {
		return unavailable()
	}
	if _, bounded := ctx.Deadline(); !bounded || ctx.Err() != nil {
		return unavailable()
	}
	if opt.ResultPort != "" {
		port, err := strconv.ParseUint(opt.ResultPort, 10, 16)
		if err != nil || port == 0 {
			return unavailable()
		}
		opt.ResultPort = strconv.FormatUint(port, 10)
	}
	// Content opt-in opens only the answer/judge sink, never subprocess logs
	// or toolchain notices. Copies retain the caller's cancellation and inputs.
	worker := *a
	worker.Out, worker.Err = io.Discard, io.Discard
	runner := *a.Run
	runner.Stdout, runner.Stderr, runner.Echo = io.Discard, io.Discard, false
	worker.Run = &runner
	owned := placement.Resources[1]
	ref := agentruntime.AgentRef{Namespace: owned.Namespace, Name: owned.Name, UID: owned.UID}
	receipt, err := worker.evaluateOrkaNative(ctx, ref, c, orkaNativeEvaluationOptions{
		resultPort: opt.ResultPort, judge: judge, out: content, strictAttribution: true,
		verify: func(ctx context.Context) error { return worker.verifySuiteNative(ctx, placement) },
	})
	if err != nil {
		return unavailable()
	}
	// Legacy expectation operands belong only to legacy terminal rendering.
	receipt.Matched, receipt.Missing = nil, nil
	result.EvaluationReceipt = receipt
	if _, err := fmt.Fprintf(a.Out, "%s: %s\n", c.ID, result.Verdict); err != nil {
		return result, fmt.Errorf("native suite case summary unavailable")
	}
	return result, nil
}
