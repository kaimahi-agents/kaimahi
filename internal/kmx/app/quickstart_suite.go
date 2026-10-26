package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Preparation is not deployment. Apply consumes this opaque plan separately;
// only successful verified placement authorizes the subsequent fresh answer.
type preparedQuickstartSuite struct {
	Plan                        *PreparedReleaseLift
	Workspace, Layout, Evidence string
}

func quickstartSuiteState() (string, error) {
	return privateWorkspaceState("quickstart")
}

func privateWorkspaceState(name string) (string, error) {
	state, err := config.StateDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(state, name)
	// Refuse linked ancestors before creating local source or exclusive evidence.
	for path := base; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return "", errors.New("quickstart state requires unlinked directories")
		}
		if path == filepath.Dir(path) {
			break
		}
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("quickstart state must be private")
	}
	return base, nil
}

// Fixed source remains visible and is never overwritten, including authored
// cases. An independently generated candidate pins the complete expected S.
func (a *App) quickstartSuiteSource(ctx context.Context) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if a.Cfg == nil {
		return "", "", errors.New("quickstart requires configured exact model")
	}
	base, err := quickstartSuiteState()
	if err != nil {
		return "", "", err
	}
	stage, err := os.MkdirTemp(base, "source-check-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	input := filepath.Join(stage, "instructions.md")
	if err := os.WriteFile(input, []byte(scaffold.DefaultOrkaInstructions(QuickstartAgent)), 0600); err != nil {
		return "", "", err
	}
	opt := workspace.CreateOptions{Name: QuickstartAgent, Model: a.Cfg.Model, Description: "The agent kmx quickstart deploys to answer a first question", InstructionsFile: input}
	candidate := filepath.Join(stage, "workspace")
	if err := workspace.Create(ctx, candidate, opt); err != nil {
		return "", "", err
	}
	state, err := config.StateDir()
	if err != nil {
		return "", "", err
	}
	cache := filepath.Join(state, "suites")
	wanted, err := workspace.Prepare(ctx, candidate, cache)
	if err != nil {
		return "", "", err
	}
	root := filepath.Join(base, QuickstartAgent)
	_, err = workspace.Resolve("", root)
	if errors.Is(err, workspace.ErrNotFound) {
		if err := workspace.Create(ctx, root, opt); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	actual, err := workspace.Prepare(ctx, root, cache)
	if err != nil {
		return "", "", err
	}
	if actual.Descriptor.Digest != wanted.Descriptor.Digest {
		return "", "", fmt.Errorf("quickstart workspace %q differs from its fixed source; keep your edits and use explicit eval, push and deploy, or select a separate KMX_HOME for a fresh quickstart", root)
	}
	return root, wanted.Descriptor.Digest.String(), nil
}

func (a *App) prepareQuickstartSuite(ctx context.Context, inference, resultPort string) (*preparedQuickstartSuite, error) {
	root, expectedS, err := a.quickstartSuiteSource(ctx)
	if err != nil {
		return nil, err
	}
	return a.prepareWorkspaceSuiteLift(ctx, suiteWorkspaceLiftOptions{Workspace: root, ExpectedSuite: expectedS, Namespace: a.orkaSetupTaskNamespace(), Inference: inference, ResultPort: resultPort, StrictReuse: true})
}

type suiteWorkspaceLiftOptions struct {
	Workspace, ExpectedSuite, ExpectedFingerprint, Namespace, Inference, ResultPort string
	StrictReuse                                                                     bool
}

func (a *App) prepareWorkspaceSuiteLift(ctx context.Context, opt suiteWorkspaceLiftOptions) (*preparedQuickstartSuite, error) {
	base, err := quickstartSuiteState()
	if err != nil {
		return nil, err
	}
	// Keep passing evidence after errors for diagnosis, but never reuse it as the
	// next invocation's output. It is outside source and the prepared cache.
	run, err := os.MkdirTemp(base, "run-")
	if err != nil {
		return nil, err
	}
	evidence := filepath.Join(run, "passing.json")
	p, err := PrepareSuiteWorkspaceEvaluation(ctx, SuiteWorkspaceEvaluationOptions{Workspace: opt.Workspace, ToContext: a.Cfg.KubeContext, ToNamespace: opt.Namespace, Inference: opt.Inference, Out: evidence, ResultPort: opt.ResultPort})
	if err != nil {
		return nil, err
	}
	defer p.Close()
	if p.prepared.Descriptor.Digest.String() != opt.ExpectedSuite || opt.ExpectedFingerprint != "" && p.prepared.Fingerprint != opt.ExpectedFingerprint {
		return nil, errors.New("reviewed source changed before evaluation; review the edited workspace again")
	}
	// Intermediate operation summaries belong to diagnostics, not the answer or
	// single JSON result. The subprocess runner follows the same stream rule.
	worker := *a.withRunContext(ctx)
	// The first-answer requirement does not authorize protected suite evaluation.
	worker.taskEnforcement = nil
	worker.Out = worker.Err
	if worker.Out == nil {
		worker.Out, worker.Err = io.Discard, io.Discard
	}
	runner := *worker.Run
	runner.Stdout, runner.Stderr = worker.Err, worker.Err
	worker.Run = &runner
	started := a.timeNow()
	if err := worker.evaluateSuiteWorkspace(ctx, p, opt.StrictReuse); err != nil {
		return nil, err
	}
	a.notef("Full suite evaluation completed (%s)", a.timeNow().Sub(started).Round(time.Millisecond))
	layout := filepath.Join(base, "layout")
	published, err := worker.PushSuiteWorkspaceEvaluated(ctx, opt.Workspace, "workspace:"+p.suite.MemberID, layout, false, false, SuitePushProvenance{}, evidence)
	if err != nil {
		return nil, err
	}
	started = a.timeNow()
	plan, err := worker.PrepareReleaseLift(ctx, ReleaseLiftOptions{Reference: "workspace@" + published.Descriptor.Digest.String(), FromLayout: layout, ToContext: a.Cfg.KubeContext, ToNamespace: p.options.ToNamespace, Inference: opt.Inference, Evidence: evidence})
	if err != nil {
		return nil, err
	}
	a.notef("Receipt-bound deploy planning completed (%s)", a.timeNow().Sub(started).Round(time.Millisecond))
	return &preparedQuickstartSuite{Plan: plan, Workspace: opt.Workspace, Layout: layout, Evidence: evidence}, nil
}

func (a *App) stepQuickstartSuite() error {
	ctx, cancel := context.WithTimeout(a.operationContext(), 10*time.Minute)
	defer cancel()
	p, err := a.prepareQuickstartSuite(ctx, "provider:local", a.quickstartResultPort)
	if err != nil {
		return err
	}
	result, err := a.applyWorkspaceSuiteLift(ctx, p, p.Plan.Summary().PlanDigest)
	if err != nil {
		return err
	}
	a.quickstartPrepared, a.quickstartPlacement = p, &result
	return nil
}

func (a *App) writeSuiteLiftSummary(p *preparedQuickstartSuite) error {
	s := p.Plan.Summary()
	_, err := fmt.Fprintf(a.Err, "AgentSuite: %s; member: %s; runtime: %s\nDestination: %s / %s; inference: %s\nEvidence: %s; gate: %s\nReview: %s\n", s.SuiteDigest, safeTerminal(s.Member), s.Runtime, safeTerminal(s.Target.Context), s.Target.Namespace, safeTerminal(s.Inference), s.EvidenceRunID, s.Gate, s.PlanDigest)
	return err
}

// Review decisions are not outcomes. Preserve even partial acknowledged writes
// in diagnostics, and never turn an output failure into successful placement.
func (a *App) applyWorkspaceSuiteLift(ctx context.Context, p *preparedQuickstartSuite, reviewedDigest string) (ReleaseLiftResult, error) {
	if err := a.writeSuiteLiftSummary(p); err != nil {
		return ReleaseLiftResult{State: "not-applied"}, fmt.Errorf("write deploy review before apply: %w", err)
	}
	started := a.timeNow()
	result, err := a.ApplyReleaseLift(ctx, p.Plan, reviewedDigest)
	a.notef("Deploy finished with state %s (%s)", result.State, a.timeNow().Sub(started).Round(time.Millisecond))
	for _, row := range result.Resources {
		if _, outputErr := fmt.Fprintf(a.Err, "%s/%s: %s; observed Ready=%t; UID=%s\n", row.Kind, safeTerminal(row.Name), row.Outcome, row.Ready, safeTerminal(row.UID)); outputErr != nil {
			return result, errors.Join(err, fmt.Errorf("deploy state %s; reporting failed and completed resources remain: %w", result.State, outputErr))
		}
	}
	if err != nil {
		return result, err
	}
	if result.State != "ready" {
		return result, fmt.Errorf("deploy did not establish a ready placement")
	}
	return result, nil
}

func (a *App) answerSuitePlacement(ctx context.Context, result ReleaseLiftResult, task string) (string, error) {
	if result.State != "ready" {
		return "", fmt.Errorf("a successful suite placement is required before an answer")
	}
	opt := a.quickstartCreateOptions()
	opt.Name, opt.Namespace, opt.Task = result.Summary.Member, result.Summary.Target.Namespace, task
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	session, err := a.openOrkaResultSession(ctx, opt)
	if err != nil {
		return "", err
	}
	defer session.close()
	if err := a.VerifyReleaseLiftPlacement(ctx, result); err != nil {
		return "", err
	}
	return a.runQuickstartOrkaTaskProfile(ctx, opt.Name, opt.Namespace, task, nil, nil, session)
}
