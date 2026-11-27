//go:build windows

package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/workspacetest"
)

// PrepareWindowsSuiteWorkspaceEvaluation verifies the native remote-only
// kubectl boundary before the CLI freezes source or opens private Test state.
func (a *App) PrepareWindowsSuiteWorkspaceEvaluation(ctx context.Context) error {
	if a == nil || a.Cfg == nil || a.Run == nil {
		return errors.New("native workspace Test prerequisites are unavailable")
	}
	worker := a.withRunContext(ctx)
	if err := worker.preflightSuiteWorkspaceKubectl(); err != nil {
		return err
	}
	return worker.requireRemoteSuiteWorkspaceContext(ctx)
}

func unsupportedSuiteWorkspaceRuntimeError() error {
	return errors.New("native Windows workspace Test supports only explicit remote Orka evaluation")
}

func prepareSuiteWorkspacePackage(ctx context.Context, root, _ string, state string, opt SuiteWorkspaceEvaluationOptions, failFast bool) (*workspace.Prepared, *workspace.NativeSuite, *workspacetest.Prepared, error) {
	if failFast || !opt.ManagedPass || opt.Bindings != "" || opt.DevelopmentHost != "" || opt.JudgeBaseURL != "" || opt.JudgeKeyEnv != "" || opt.ResultPort != "" || opt.JSON || opt.ShowContent || opt.ExpectedRuntime != "" && opt.ExpectedRuntime != "orka" {
		return nil, nil, nil, errors.New("native Windows workspace Test supports only explicit remote Orka evaluation")
	}
	if suiteReceiptPhysicallyInside(ctx, state, root) {
		return nil, nil, nil, errors.New("native Windows workspace Test state must stay physically outside the workspace")
	}
	operation, err := workspacetest.FreezeOrka(ctx, workspacetest.Summary{Workspace: root, Runtime: "orka", Context: opt.ToContext, Namespace: opt.ToNamespace, Inference: opt.Inference}, filepath.Join(state, "workspace-test"))
	if err != nil {
		return nil, nil, nil, err
	}
	suite, err := operation.Suite(ctx)
	if err != nil {
		_ = operation.Close()
		return nil, nil, nil, err
	}
	return nil, suite, operation, nil
}
