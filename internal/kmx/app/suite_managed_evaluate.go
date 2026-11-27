package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The evaluator may export an explicit receipt, but export alone cannot create
// managed eligibility: deferred runtime/client and output closes can still fail.
func (p *PreparedSuiteWorkspaceEvaluation) publishPassingReceipt(ctx context.Context, receipt *suitePassingReceipt) error {
	raw, err := canonicalSuitePassingReceipt(ctx, receipt, p.suite)
	if err != nil {
		return err
	}
	if !p.options.ManagedPass || p.options.Out != "" {
		if err := p.output.publishBytes(ctx, raw); err != nil {
			return err
		}
	} else if err := p.output.unchanged(); err != nil {
		return err
	}
	if p.options.ManagedPass {
		p.completed = raw
	}
	return nil
}

func (p *PreparedSuiteWorkspaceEvaluation) reportPassingReceipt(out io.Writer) error {
	var err error
	if p.options.ManagedPass && p.options.Out == "" {
		_, err = fmt.Fprintln(out, "Full passing native evaluation complete; managed proof registration pending.")
	} else {
		_, err = fmt.Fprintf(out, "Passing native receipt: %q (unsigned observations, not authentication or a policy gate)\n", p.options.Out)
	}
	if err != nil {
		return errSuitePassingExport
	}
	return nil
}

func (a *App) finishManagedEvaluation(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation, evaluationErr error) (err error) {
	// This is outside the evaluator call, after all of its deferred closes.
	// Keep this fence for both publishers: registration must not name a moved
	// explicit receipt even when legacy Unix linking itself completed.
	var outputFenceErr error
	if p.output != nil && p.output.linked {
		if outputFenceErr = p.output.unchanged(); outputFenceErr != nil {
			p.output.namedPathCurrent = false
			outputFenceErr = errSuitePassingExport
		}
	}
	linked := p.output != nil && p.output.linked && p.output.namedPathCurrent
	linkedAtMovedParent := p.output != nil && p.output.linked && !p.output.namedPathCurrent
	defer func() {
		p.explicitReceiptMayExist = err != nil && p.options.Out != "" && linked
	}()
	closeErr := p.closeOutput()
	p.outputFinalizationFailed = closeErr != nil
	if p.outputFinalizationFailed {
		p.runs.ThresholdUnreachable = false
	}
	err = errors.Join(evaluationErr, outputFenceErr, closeErr)
	if err == nil {
		err = p.checkCurrent(ctx)
	}
	if err == nil && p.bindings != nil {
		err = p.bindings.recheck()
	}
	if err == nil && p.sessions != nil {
		err = p.sessions.recheck(p.suite)
	}
	if err == nil && len(p.completed) == 0 {
		err = errSuitePassingReceipt
	}
	if err != nil {
		if p.options.Out != "" {
			if linkedAtMovedParent {
				return fmt.Errorf("evaluation not registered; explicit receipt parent changed after observed publication; inspect private state before retrying: %w", err)
			}
			return fmt.Errorf("evaluation not registered; explicit receipt output may exist at %q: %w", p.options.Out, err)
		}
		return err
	}
	// Completion follows runtime cleanup, deferred closes and final input fences.
	if p.publicMetadata != nil {
		p.publicMetadata.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var warningErr error
	if metadataErr := p.store.publishPublicMetadata(ctx, p.completed, p.suite, p.publicMetadata); metadataErr != nil {
		// Public metadata must never poison ordinary private completion. Retain
		// reporting failure until after commit, without exposing writer causes.
		if a.Err != nil {
			if _, e := fmt.Fprintln(a.Err, metadataErr); e != nil {
				warningErr = errors.New("public metadata warning reporting failed; inspect private managed proof before re-evaluating")
			}
		}
	}
	p.record, err = p.store.commit(ctx, p.completed, p.suite)
	p.completed = nil
	err = errors.Join(err, warningErr)
	if err != nil {
		return managedProofCommitError(p, err)
	}
	diagnostics := a.Err
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	if _, err := fmt.Fprintf(diagnostics, "Managed passing evidence: %q; AgentSuite: %s (unsigned historical observations)\n", p.record, p.suite.Digest); err != nil {
		return fmt.Errorf("completed managed proof saved at %q; reporting failed: %w", p.record, err)
	}
	return nil
}

func managedProofCommitError(p *PreparedSuiteWorkspaceEvaluation, err error) error {
	if p.record == "" && p.options.Out != "" {
		var uncertain *suitePassRegistrationUncertain
		if errors.As(err, &uncertain) {
			return fmt.Errorf("managed proof registration is uncertain; complete explicit receipt exists at %q; inspect private state before retrying: %w", p.options.Out, err)
		}
		return fmt.Errorf("managed proof not registered; complete explicit receipt exists at %q: %w", p.options.Out, err)
	}
	return err
}
