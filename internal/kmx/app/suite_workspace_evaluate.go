package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// SuiteWorkspaceEvaluationOptions selects a full native workspace invocation.
// Runtime, model and assertions remain packaged; judge transport is explicit.
type SuiteWorkspaceEvaluationOptions struct {
	Workspace, ToContext, ToNamespace, Inference, Bindings, Out, ResultPort, DevelopmentHost string
	OrkaChart, OrkaChartSHA256                                                               string
	CaseTimeout                                                                              time.Duration
	JudgeBaseURL, JudgeKeyEnv                                                                string
	// ExpectedRuntime is a destination declaration, never a runtime override.
	ExpectedRuntime string
	// ManagedPass is public root eval's private completed-proof registration.
	// Convenience evaluators keep their existing explicit output behavior.
	ManagedPass bool
	// JSON selects the Orka-only run-local report, not receipt content.
	JSON bool
	// ShowContent opts into sanitized answer/operand/rationale diagnostics on stderr.
	ShowContent bool
}

// PreparedSuiteWorkspaceEvaluation owns its offline source snapshot and anchored
// exclusive export destination. Its contents are not a public mutable plan.
type PreparedSuiteWorkspaceEvaluation struct {
	prepared       *workspace.Prepared
	suite          *workspace.NativeSuite
	inventory      []suitePassingInventoryCase
	output         *suitePassingExport
	root           string
	options        SuiteWorkspaceEvaluationOptions
	started        bool
	bindings       *suiteKagentBindingSnapshot
	sessions       *suiteSessionsHostSnapshot
	judge          agentruntime.EvaluationJudge
	store          *suitePassStore
	completed      []byte
	record         string
	candidateRun   string
	runs           suiteRunSummary
	cleanup        string
	publicMetadata *suitePublicMetadata
	failures       []suiteFailureDetail
	// Composed Lift keeps its real diagnostic writer separate from quiet stage workers.
	failureWriter io.Writer
	diagnosticKey string
}

func (p *PreparedSuiteWorkspaceEvaluation) Close() error {
	if p == nil {
		return nil
	}
	err := p.closeOutput()
	if p.store != nil {
		err = errors.Join(err, p.store.Close())
		p.store = nil
	}
	if err != nil && p.record != "" {
		return fmt.Errorf("completed managed proof saved at %q; finalization failed: %w", p.record, err)
	}
	return err
}

func (p *PreparedSuiteWorkspaceEvaluation) closeOutput() error {
	if p.output == nil {
		return nil
	}
	output := p.output
	p.output = nil
	return output.Close()
}

// PrepareSuiteWorkspaceEvaluation must run before loading cluster settings.
func PrepareSuiteWorkspaceEvaluation(ctx context.Context, opt SuiteWorkspaceEvaluationOptions) (*PreparedSuiteWorkspaceEvaluation, error) {
	if err := validateOrkaNativeChartSelection(opt.OrkaChart, opt.OrkaChartSHA256); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opt.CaseTimeout == 0 {
		opt.CaseTimeout = 5 * time.Minute
	}
	if (opt.Out == "" && !opt.ManagedPass) || opt.CaseTimeout <= 0 || opt.CaseTimeout > 9*time.Minute {
		return nil, errors.New("native evaluation requires private output and bounded case timeout")
	}
	if opt.DevelopmentHost != "" {
		if opt.ToContext != "" || opt.ToNamespace != "" || opt.Inference != "" || opt.ResultPort != "" {
			return nil, errors.New("development host evaluation refuses cluster, inference and result selectors")
		}
	} else if !verificationIdentity(opt.ToContext) || scaffold.RefuseKeyShapes(opt.ToContext) != nil || (opt.Inference == "") == (opt.Bindings == "") {
		return nil, errors.New("native evaluation requires explicit context, exactly one inference or bindings selector, or an explicit development host")
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
	if opt.Out != "" && suiteReceiptInside(opt.Out, filepath.Join(state, "passing")) {
		return nil, fmt.Errorf("receipt output must stay outside managed passing state: %w", errSuitePassingFile)
	}
	if opt.ManagedPass && suiteReceiptInside(filepath.Join(state, "passing"), root) {
		return nil, errSuitePassStore
	}
	prepared, err := workspace.Prepare(ctx, root, cache)
	if err != nil {
		return nil, suiteEvaluationError(ctx, err, false)
	}
	suite, err := workspace.Load(ctx, prepared)
	if err != nil {
		return nil, suiteEvaluationError(ctx, err, false)
	}
	if opt.OrkaChart != "" && (suite.Runtime.ID != "orka" || opt.DevelopmentHost != "") {
		return nil, errors.New("local Orka chart selection requires native Orka workspace evaluation")
	}
	if opt.JSON && suite.Runtime.ID != "orka" {
		return nil, errors.New("native eval JSON output supports only Orka")
	}
	if opt.ExpectedRuntime != "" && opt.ExpectedRuntime != suite.Runtime.ID {
		return nil, errors.New("destination runtime declaration does not match packaged runtime")
	}
	var sessions *suiteSessionsHostSnapshot
	if suite.Runtime.ID == "agentsessions" {
		if opt.DevelopmentHost == "" {
			return nil, errors.New("unsupported native suite runtime without explicit --development-host")
		}
		if opt.Bindings == "" {
			return nil, errSuiteSessionsBinding
		}
		// The suite pins the model; the binding must serve exactly that model.
		binding, err := snapshotSuiteSessionsBinding(opt.Bindings, suite)
		if err != nil {
			return nil, err
		}
		sessions, err = snapshotSuiteSessionsHost(opt.DevelopmentHost, suite, binding)
		if err != nil {
			return nil, err
		}
	} else if opt.DevelopmentHost != "" || (suite.Runtime.ID != "orka" && suite.Runtime.ID != "kagent") {
		return nil, errors.New("development host requires the packaged agentsessions runtime")
	}
	var bindings *suiteKagentBindingSnapshot
	if suite.Runtime.ID == "agentsessions" {
		// The host and binding are already explicitly snapshotted; no Kubernetes defaults apply.
	} else if suite.Runtime.ID == "kagent" {
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
		provider := strings.TrimPrefix(opt.Inference, "provider:")
		port, portErr := strconv.Atoi(opt.ResultPort)
		if opt.Bindings != "" || !strings.HasPrefix(opt.Inference, "provider:") || scaffold.ValidateObjectName(provider) != nil || scaffold.ValidateNamespace(opt.ToNamespace) != nil || (opt.ResultPort != "" && (portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != opt.ResultPort)) {
			return nil, errors.New("native evaluation requires Orka provider:NAME, valid namespace and port")
		}
	}
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		return nil, err
	}
	cases := make([]agentruntime.EvaluationCase, len(inventory))
	for i, frame := range inventory {
		cases[i] = frame.Case
	}
	var diagnosticKey string
	judge, err := newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: opt.JudgeBaseURL, KeyEnv: opt.JudgeKeyEnv, diagnosticKey: &diagnosticKey}, cases, io.Discard)
	if err != nil {
		return nil, err
	}
	p := &PreparedSuiteWorkspaceEvaluation{prepared: prepared, suite: suite, inventory: inventory, root: root, options: opt, bindings: bindings, sessions: sessions, judge: judge, diagnosticKey: diagnosticKey, runs: suiteRunSummary{Total: len(inventory), SuiteRunExecutionSummary: SuiteRunExecutionSummary{NotExecuted: len(inventory)}}, cleanup: "candidate cleanup not required"}
	if opt.ManagedPass {
		p.store, err = openSuitePassStore(ctx, root, cache, suite.Digest)
		if err == nil {
			err = p.store.reserve(ctx)
		}
		if err != nil {
			return nil, errors.Join(err, p.Close())
		}
	}
	destination := opt.Out
	if destination == "" {
		// Retain the same preflight/identity checks without writing an
		// uncommitted managed receipt. Canonical bytes stay only in memory.
		destination = filepath.Join(p.store.partition, ".pending-receipt")
	}
	p.output, err = preflightSuitePassingExport(ctx, destination, root, cache)
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
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
	apiNamespace, name, err := a.orkaResultAPI(ctx, ns, "")
	if err != nil {
		return err
	}
	raw, err = a.orkaCapture(ctx, nil, "-n", apiNamespace, "get", "service", name, "-o", "json")
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
// only full passing metadata. Orka uses run-scoped candidates and cleanup;
// convenience callers separately retain the internal canonical evaluator.
func (a *App) EvaluateSuiteWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) error {
	if p != nil {
		out := a.Err
		if p.failureWriter != nil {
			out = p.failureWriter
		}
		ctx = withSuiteFailureReporting(ctx, &p.failures, out, p.options.ShowContent, p.diagnosticKey)
		ctx = withSuiteFailureRetention(ctx, p.root, p.suite)
	}
	if p == nil || !p.options.JSON {
		return a.evaluateSuiteWorkspaceWithProof(ctx, p)
	}
	// JSON owns stdout. Keep the evaluator, guard and proof lifecycle unchanged,
	// routing their text diagnostics separately from the one safe report.
	worker := *a
	worker.Out = a.Err
	if worker.Out == nil {
		worker.Out = io.Discard
	}
	err := worker.evaluateSuiteWorkspaceWithProof(ctx, p)
	report := struct {
		SuiteRunExecutionSummary
		Status   string               `json:"status"`
		Detail   string               `json:"detail"`
		Passed   int                  `json:"passed"`
		Total    int                  `json:"total"`
		Cleanup  string               `json:"cleanup"`
		Error    string               `json:"error,omitempty"`
		Failures []suiteFailureDetail `json:"failures,omitempty"`
	}{Failures: suiteFailureSummaries(p.failures), SuiteRunExecutionSummary: p.runs.SuiteRunExecutionSummary, Status: "complete", Detail: p.runs.String() + "; " + p.cleanup, Passed: p.runs.Passed, Total: p.runs.Total, Cleanup: p.cleanup}
	if err != nil {
		report.Status = "failed"
		// Never serialize causes: transports, writers and runtime errors can
		// carry content. Publication/finalization failures may leave proof.
		report.Error = "native evaluation failed; inspect diagnostics and private proof state before retrying"
		if p.runs.Aborted || p.runs.ReportingFailed {
			report.Detail += "; no passing evidence published"
		}
	}
	return errors.Join(err, json.NewEncoder(a.Out).Encode(report))
}

func (a *App) evaluateSuiteWorkspaceWithProof(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) error {
	if p != nil && p.options.ManagedPass {
		if p.store == nil || p.store.unchanged() != nil {
			return errSuitePassStore
		}
		p.publicMetadata = captureSuitePublicMetadata()
		return a.finishManagedEvaluation(ctx, p, a.evaluateSuiteWorkspaceSelected(ctx, p))
	}
	return a.evaluateSuiteWorkspaceSelected(ctx, p)
}

func (a *App) evaluateSuiteWorkspaceSelected(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) error {
	if p != nil && p.suite != nil && p.suite.Runtime.ID == "agentsessions" {
		return a.evaluateSuiteSessionsWorkspace(ctx, p)
	}
	if p != nil && p.suite != nil && p.suite.Runtime.ID == "orka" {
		return a.evaluateSuiteWorkspaceCandidate(ctx, p)
	}
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
	if p == nil || p.suite == nil || (p.suite.Runtime.ID != "orka" && p.suite.Runtime.ID != "kagent") || p.output == nil || p.started || a.Cfg == nil || a.Run == nil || a.Cfg.KubeContext != p.options.ToContext {
		return errSuiteNativeEvaluation
	}
	p.started = true
	for _, frame := range p.inventory {
		if frame.Metadata.Runs > 1 {
			if _, err := fmt.Fprintf(a.Err, "Run preflight: %d fresh Tasks, %s per run, %s total execution budget (upper bound; excludes placement; cancellation can stop earlier).\n", len(p.inventory), p.options.CaseTimeout, time.Duration(len(p.inventory))*p.options.CaseTimeout); err != nil {
				return err
			}
			break
		}
	}
	if p.judge != nil {
		count := 0
		for _, frame := range p.inventory {
			for _, assertion := range frame.Case.Assertions {
				if assertion.Type == "llmJudge" {
					count += 2
				}
			}
		}
		if _, err := fmt.Fprintf(a.Err, "Judge preflight: %d independent requests over case inputs, attributed answers and authored rubrics; shares each run's deadline. Identity/version are user-declared, not provider attestation. Endpoint omitted from recovery advice; supply it again. Non-pass reasoning is diagnostic-only; --show-content opts into sanitized bounded content on stderr.\n", count); err != nil {
			return err
		}
	}
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
	if err := worker.selectOrkaNativeCandidate(ctx, p.options.OrkaChart, p.options.OrkaChartSHA256, p.options.ToNamespace); err != nil {
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
	observations, err := evaluateSuiteRuns(ctx, p.inventory, p.options.CaseTimeout, worker.Out, func(caseCtx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
		if err := workspace.CheckCurrent(caseCtx, p.root, p.prepared); err != nil {
			return suitePassingObservation{}, err
		}
		return worker.evaluateSuiteNative(caseCtx, placement, frame, p.options.ResultPort, p.judge)
	})
	if err != nil {
		return err
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
	if err := p.publishPassingReceipt(ctx, receipt); err != nil {
		return err
	}
	return p.reportPassingReceipt(worker.Out)
}
