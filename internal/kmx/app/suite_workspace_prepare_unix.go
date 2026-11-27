//go:build !windows

package app

import (
	"context"
	"errors"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/workspacetest"
)

// PrepareWindowsSuiteWorkspaceEvaluation is unreachable through the Unix
// command adapter and grants no cross-platform behavior.
func (*App) PrepareWindowsSuiteWorkspaceEvaluation(context.Context) error {
	return errors.New("native Windows workspace Test is unavailable")
}

func unsupportedSuiteWorkspaceRuntimeError() error {
	return errors.New("--fail-fast supports only standalone native Orka package test")
}

func prepareSuiteWorkspacePackage(ctx context.Context, root, cache, _ string, opt SuiteWorkspaceEvaluationOptions, failFast bool) (*workspace.Prepared, *workspace.NativeSuite, *workspacetest.Prepared, error) {
	var prepared *workspace.Prepared
	var err error
	if failFast {
		prepared, err = workspace.PrepareExactRuntime(ctx, root, cache, workspace.RuntimeProfile{ID: "orka"})
	} else {
		prepared, err = workspace.Prepare(ctx, root, cache)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	suite, err := workspace.Load(ctx, prepared)
	if err != nil {
		return nil, nil, nil, err
	}
	operation, err := workspacetest.New(workspacetest.Summary{Workspace: root, SuiteDigest: suite.Digest, SourceFingerprint: prepared.Fingerprint, Runtime: suite.Runtime.ID, Context: opt.ToContext, Namespace: opt.ToNamespace, Inference: opt.Inference})
	if err != nil {
		return nil, nil, nil, err
	}
	return prepared, suite, operation, nil
}
