package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// SuiteWorkspaceEvaluationOptions selects a full native workspace invocation.
// No runtime, model, judge or subset can override its packaged behavior.
type SuiteWorkspaceEvaluationOptions struct {
	Workspace, ToContext, ToNamespace, Inference, Bindings, Out, ResultPort string
	CaseTimeout                                                             time.Duration
}

// PreparedSuiteWorkspaceEvaluation owns its offline source snapshot and anchored
// exclusive export destination. Its contents are not a public mutable plan.
type PreparedSuiteWorkspaceEvaluation struct {
	prepared  *workspace.Prepared
	suite     *workspace.NativeSuite
	inventory []suitePassingInventoryCase
	output    *suitePassingExport
	root      string
	options   SuiteWorkspaceEvaluationOptions
	started   bool
	bindings  *suiteKagentBindingSnapshot
}

func (p *PreparedSuiteWorkspaceEvaluation) Close() error {
	if p == nil || p.output == nil {
		return nil
	}
	output := p.output
	p.output = nil
	return output.Close()
}

// PrepareSuiteWorkspaceEvaluation must run before loading cluster settings.
func PrepareSuiteWorkspaceEvaluation(ctx context.Context, opt SuiteWorkspaceEvaluationOptions) (*PreparedSuiteWorkspaceEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opt.CaseTimeout == 0 {
		opt.CaseTimeout = 5 * time.Minute
	}
	if !verificationIdentity(opt.ToContext) || scaffold.RefuseKeyShapes(opt.ToContext) != nil || (opt.Inference == "") == (opt.Bindings == "") || opt.Out == "" || opt.CaseTimeout <= 0 || opt.CaseTimeout > 9*time.Minute {
		return nil, errors.New("native evaluation requires explicit context, exactly one inference or bindings selector, private output and bounded case timeout")
	}
	root, err := resolveSuiteWorkspace(opt.Workspace)
	if err != nil {
		// The fixed missing-workspace hint is safe; malformed descriptor
		// member validation can quote decoded source, so do not expose it.
		if errors.Is(err, workspace.ErrNotFound) {
			return nil, err
		}
		return nil, suiteEvaluationError(ctx, err, false)
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, suiteEvaluationError(ctx, err, false)
	}
	cache := filepath.Join(state, "suites")
	prepared, err := workspace.Prepare(ctx, root, cache)
	if err != nil {
		return nil, suiteEvaluationError(ctx, err, false)
	}
	suite, err := workspace.Load(ctx, prepared)
	if err != nil {
		return nil, suiteEvaluationError(ctx, err, false)
	}
	var bindings *suiteKagentBindingSnapshot
	if suite.Runtime.ID == "kagent" {
		if opt.Bindings == "" || opt.ResultPort != "" {
			return nil, errors.New("native kagent evaluation requires --bindings and refuses --result-port")
		}
		bindings, err = snapshotSuiteKagentBindings(opt.Bindings)
		if err != nil {
			return nil, err
		}
		if opt.ToNamespace != "" && opt.ToNamespace != bindings.binding.Namespace {
			return nil, errors.New("native kagent namespace must agree with bindings")
		}
		opt.ToNamespace = bindings.binding.Namespace
	} else {
		if opt.ToNamespace == "" {
			opt.ToNamespace = OrkaNamespace
		}
		if opt.ResultPort == "" {
			opt.ResultPort = "19180"
		}
		provider := strings.TrimPrefix(opt.Inference, "provider:")
		port, portErr := strconv.Atoi(opt.ResultPort)
		if opt.Bindings != "" || !strings.HasPrefix(opt.Inference, "provider:") || scaffold.ValidateObjectName(provider) != nil || scaffold.ValidateNamespace(opt.ToNamespace) != nil || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != opt.ResultPort {
			return nil, errors.New("native evaluation requires Orka provider:NAME, valid namespace and port")
		}
	}
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		return nil, err
	}
	output, err := preflightSuitePassingExport(ctx, opt.Out, root, cache)
	if err != nil {
		return nil, err
	}
	return &PreparedSuiteWorkspaceEvaluation{prepared: prepared, suite: suite, inventory: inventory, output: output, root: root, options: opt, bindings: bindings}, nil
}

// Check the read-only session prerequisites before pair writes. The bearer and
// pinned socket are acquired per case, never held through a long Ready wait.
func (a *App) preflightSuiteNativeResults(ctx context.Context, ns string) error {
	raw, err := a.orkaCapture(ctx, nil, "-n", ns, "get", "serviceaccount", orkaResultAccount, "--ignore-not-found=true", "-o", "name")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "serviceaccount/"+orkaResultAccount {
		return errSuiteNativeEvaluation
	}
	name, err := a.orkaAPIService(ctx, ns)
	if err != nil {
		return err
	}
	raw, err = a.orkaCapture(ctx, nil, "-n", ns, "get", "service", name, "-o", "json")
	if err != nil {
		return err
	}
	var service struct {
		Spec struct{ Ports []struct{ Port int } }
	}
	if json.Unmarshal(raw, &service) != nil {
		return errSuiteNativeEvaluation
	}
	for _, port := range service.Spec.Ports {
		if port.Port == 8080 {
			return nil
		}
	}
	return errSuiteNativeEvaluation
}

// EvaluateSuiteWorkspace executes each original ordered case once and publishes
// only full passing metadata. Late failures leave resources, not recovery state.
func (a *App) EvaluateSuiteWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) error {
	return a.evaluateSuiteWorkspace(ctx, p, false)
}

func (a *App) evaluateSuiteWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation, strictReuse bool) (err error) {
	mayHaveChanged, guardRefusal := false, false
	defer func() {
		if err != nil && !guardRefusal {
			occupied := errors.Is(err, errSuiteKagentOccupied)
			err = suiteEvaluationError(ctx, err, mayHaveChanged)
			if occupied {
				err = errors.Join(err, errSuiteKagentOccupied)
			}
		}
	}()
	if p == nil || p.output == nil || p.started || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext != p.options.ToContext {
		return errSuiteNativeEvaluation
	}
	p.started = true
	worker := *a.withRunContext(ctx)
	worker.guarded = false
	if p.suite.Runtime.ID == "kagent" {
		return worker.evaluateSuiteKagentWorkspace(ctx, p, &mayHaveChanged, &guardRefusal)
	}
	kubectl := depKubectl
	kubectl.fetchable = false
	if err := worker.preflight(kubectl); err != nil {
		return err
	}
	if err := workspace.CheckCurrent(ctx, p.root, p.prepared); err != nil {
		return err
	}
	native, err := worker.prepareSuiteNative(ctx, p.suite, p.options.ToNamespace, strings.TrimPrefix(p.options.Inference, "provider:"))
	if err != nil {
		return err
	}
	if !validSuitePassingSubject(native.Subject) {
		return errSuitePassingReceipt
	}
	if err := coherentSuitePassingSubject(native.Subject, p.suite); err != nil {
		return err
	}
	if err := worker.preflightSuiteNativeResults(ctx, p.options.ToNamespace); err != nil {
		return err
	}
	placementAction := "create/update"
	if strictReuse {
		placementAction = "create or exactly reuse"
	}
	action := fmt.Sprintf("%s owned native suite Provider and Agent and execute %d evaluation Tasks in %s", placementAction, len(p.inventory), p.options.ToNamespace)
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: p.suite.MemberID, Namespace: p.options.ToNamespace}, action); err != nil {
		// This boundary already classifies subprocess/parse diagnostics and
		// supplies the trusted guard's exact named-confirmation retry advice.
		guardRefusal = true
		return err
	}
	if err := workspace.CheckCurrent(ctx, p.root, p.prepared); err != nil {
		return err
	}
	if err := worker.verifySuiteNativeInputs(ctx, native.Subject); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(worker.Err, "Task execution uses the selected ServiceAccount's full effective authority. v0.2.0 Task-read enforcement is unverified; v0.1.3 only authenticates result reads. Retain the namespaced Task-get grant. Discarding the token is not revocation."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(worker.Out, "Workspace: %q; AgentSuite: %s\n", p.root, p.prepared.Descriptor.Digest); err != nil {
		return err
	}
	mayHaveChanged = true
	placement, err := worker.placeSuiteNativeMode(ctx, native, strictReuse)
	if err != nil {
		return err
	}
	observations := make([]suitePassingObservation, 0, len(p.inventory))
	for _, frame := range p.inventory {
		if err := workspace.CheckCurrent(ctx, p.root, p.prepared); err != nil {
			return err
		}
		caseCtx, cancel := context.WithTimeout(ctx, p.options.CaseTimeout)
		observation, caseErr := worker.evaluateSuiteNative(caseCtx, placement, frame, p.options.ResultPort)
		cancel()
		if _, err := fmt.Fprintf(worker.Out, "%s: %s\n", frame.Case.ID, observation.Verdict); err != nil {
			return err
		}
		if caseErr != nil {
			return caseErr
		}
		observations = append(observations, observation)
	}
	if err := workspace.CheckCurrent(ctx, p.root, p.prepared); err != nil {
		return err
	}
	if err := worker.verifySuiteNative(ctx, placement); err != nil {
		return err
	}
	receipt, err := newSuitePassingReceipt(ctx, p.prepared, placement, observations)
	if err != nil {
		return err
	}
	if err := p.output.publish(ctx, receipt, p.suite); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(worker.Out, "Passing native receipt: %q (unsigned observations, not authentication or a policy gate)\n", p.options.Out); err != nil {
		return errSuitePassingExport
	}
	return nil
}
