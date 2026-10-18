package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/term"
	"oras.land/oras-go/v2/registry"
)

// WorkspaceLiftOptions contains resolved selectors only, never a live config file.
// Rendering belongs to this backend: four stderr lines, verbose diagnostics, or
// one JSON result on stdout. Promote nil selects disclosed source-only promotion.
type WorkspaceLiftOptions struct {
	Workspace                             string
	Evaluation                            SuiteWorkspaceEvaluationOptions
	Destination                           ReleaseLiftOptions
	EvalName, DestinationName, Repository string
	PlanOnly, Verbose, JSON               bool
	Promote                               *bool
	Provenance                            SuitePushProvenance
}
type WorkspaceLiftPlan struct {
	SuiteDigest        string `json:"suiteDigest"`
	Workspace          string `json:"workspace"`
	EvalName           string `json:"evalName"`
	DestinationName    string `json:"destinationName"`
	Repository         string `json:"repository"`
	EvalContext        string `json:"evalContext"`
	DestinationContext string `json:"destinationContext"`
	CaseRuns           int    `json:"caseRuns"`
	Promote            bool   `json:"promote"`
	Judging            bool   `json:"judging"`
	Disclosure         string `json:"disclosure"`
	EvalServer         string `json:"evalServer,omitempty"`
	DestinationServer  string `json:"destinationServer,omitempty"`
}
type WorkspaceLiftStage struct {
	*SuiteRunExecutionSummary
	Status string `json:"status"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
	Passed int    `json:"passed,omitempty"`
	Total  int    `json:"total,omitempty"`
}
type WorkspaceLiftResult struct {
	Plan      WorkspaceLiftPlan  `json:"plan"`
	Eval      WorkspaceLiftStage `json:"eval"`
	Push      WorkspaceLiftStage `json:"push"`
	Lift      WorkspaceLiftStage `json:"lift"`
	Placement *ReleaseLiftResult `json:"placement,omitempty"`
	Error     string             `json:"error,omitempty"`
}
type PreparedWorkspaceLift struct {
	review          *suiteWorkspaceReview
	options         WorkspaceLiftOptions
	plan            WorkspaceLiftPlan
	store           *suitePassStore
	started         bool
	kube            []byte
	completionSlots int
}

func (p *PreparedWorkspaceLift) Summary() WorkspaceLiftPlan { return p.plan }
func (p *PreparedWorkspaceLift) Close() error {
	if p == nil {
		return nil
	}
	return p.store.Close()
}

// PrepareWorkspaceLift is offline and must precede operational configuration.
// Cold plan-only performs source preparation, not proof lookup or state writes.
func PrepareWorkspaceLift(ctx context.Context, opt WorkspaceLiftOptions) (*PreparedWorkspaceLift, error) {
	if opt.Evaluation.Workspace != "" && opt.Evaluation.Workspace != opt.Workspace || opt.Evaluation.Out != "" || opt.Evaluation.JSON || opt.Evaluation.Bindings != "" || opt.Evaluation.DevelopmentHost != "" || opt.Destination.Reference != "" || opt.Destination.FromLayout != "" || opt.Destination.Evidence != "" {
		return nil, errors.New("workspace Lift requires only frozen workspace and concrete Orka selections")
	}
	ref, err := registry.ParseReference(opt.Repository)
	if err != nil || ref.Reference != "" {
		return nil, errors.New("workspace Lift requires a repository without tag or digest")
	}
	for _, s := range []string{opt.EvalName, opt.DestinationName, opt.Repository} {
		if !verificationIdentity(s) || scaffold.RefuseKeyShapes(s) != nil {
			return nil, errors.New("invalid workspace Lift selector")
		}
	}
	if opt.Evaluation.ToNamespace == "" {
		opt.Evaluation.ToNamespace = OrkaNamespace
	}
	if opt.Destination.ToNamespace == "" {
		opt.Destination.ToNamespace = OrkaNamespace
	}
	port, portErr := strconv.Atoi(opt.Evaluation.ResultPort)
	if opt.Evaluation.ResultPort != "" && (portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != opt.Evaluation.ResultPort) {
		return nil, errors.New("workspace Lift requires a valid result port")
	}
	if opt.Evaluation.CaseTimeout == 0 {
		opt.Evaluation.CaseTimeout = 5 * time.Minute
	}
	if opt.Evaluation.CaseTimeout <= 0 || opt.Evaluation.CaseTimeout > 9*time.Minute {
		return nil, errors.New("workspace Lift requires a bounded case timeout")
	}
	for _, o := range []ReleaseLiftOptions{opt.Destination, {ToContext: opt.Evaluation.ToContext, ToNamespace: opt.Evaluation.ToNamespace, Inference: opt.Evaluation.Inference}} {
		o.Reference = opt.Repository + "@sha256:" + strings.Repeat("a", 64)
		o.Evidence = "private"
		if o.Bindings != "" || ValidateReleaseLiftOptions(o) != nil {
			return nil, errors.New("workspace Lift requires explicit Orka context, namespace and inference selectors")
		}
	}
	review, err := (&App{}).reviewSuiteWorkspace(ctx, opt.Workspace)
	if err != nil {
		return nil, err
	}
	for _, runtime := range []string{opt.Evaluation.ExpectedRuntime, opt.Destination.ExpectedRuntime} {
		if runtime != "" && runtime != "orka" {
			return nil, errors.New("destination runtime declaration does not match packaged Orka runtime")
		}
	}
	promote := true
	if opt.Promote != nil {
		promote = *opt.Promote
	}
	opt.Destination.Promote = promote
	opt.Evaluation.Workspace = review.root
	opt.Evaluation.ManagedPass = true
	plan := WorkspaceLiftPlan{SuiteDigest: review.suite.Digest, Workspace: review.root, EvalName: opt.EvalName, DestinationName: opt.DestinationName, Repository: opt.Repository, EvalContext: opt.Evaluation.ToContext, DestinationContext: opt.Destination.ToContext, CaseRuns: len(review.inventory), Promote: promote}
	plan.Disclosure = "eval,push,lift; fresh Tasks and UID-safe candidate deletion; publication of source/cases; runtime/model retention may outlive cleanup"
	if promote {
		plan.Disclosure += "; source-only promotion (not evaluated on destination)"
	} else {
		plan.Disclosure += "; strict evidence subject equality"
	}
	for _, frame := range review.inventory {
		for _, assertion := range frame.Case.Assertions {
			if assertion.Type == "llmJudge" {
				plan.Judging = true
			}
		}
	}
	if plan.Judging {
		plan.Disclosure += "; judging transfers case inputs, answers and rubrics to configured judge"
	}
	p := &PreparedWorkspaceLift{review: review, options: opt, plan: plan}
	if !opt.PlanOnly {
		state, e := config.StateDir()
		if e != nil {
			return nil, e
		}
		p.store, err = openSuitePassStore(ctx, review.root, filepath.Join(state, "suites"), review.suite.Digest)
	}
	return p, err
}

// LiftWorkspace is single-use. Completion observations authorize only GET-only
// skips; no persisted result can enter Apply or manufacture receipt reuse.
func (a *App) LiftWorkspace(ctx context.Context, p *PreparedWorkspaceLift) (r WorkspaceLiftResult, err error) {
	if p == nil || p.started {
		return r, errors.New("fresh prepared workspace Lift required")
	}
	p.started = true
	r.Plan = p.plan
	r.Eval = WorkspaceLiftStage{Status: "not-attempted", Detail: "not attempted", Total: p.plan.CaseRuns}
	r.Push = WorkspaceLiftStage{Status: "not-attempted", Detail: "not attempted; nothing published"}
	r.Lift = WorkspaceLiftStage{Status: "not-attempted", Detail: p.plan.DestinationName + " unchanged"}
	diagnostics := a.Err
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	planWritten, planOpen := false, false
	writePlan := func() error {
		planWritten = true
		ending := "\n"
		if !p.options.PlanOnly && !p.options.JSON && a.Cfg != nil && a.Cfg.Confirm == "" && a.Stdin != nil && term.IsTerminal(int(a.Stdin.Fd())) {
			ending = ""
			planOpen = true
		}
		_, e := fmt.Fprintf(diagnostics, "Plan · %d case runs on %s (%s server=%s namespace=%s) → %s → %s (%s server=%s namespace=%s) · %s%s", p.plan.CaseRuns, p.plan.EvalName, p.plan.EvalContext, r.Plan.EvalServer, p.options.Evaluation.ToNamespace, p.plan.Repository, p.plan.DestinationName, p.plan.DestinationContext, r.Plan.DestinationServer, p.options.Destination.ToNamespace, r.Plan.Disclosure, ending)
		return e
	}
	defer func() {
		if e := p.Close(); e != nil {
			err = errors.Join(err, fmt.Errorf("completed effects retained; workspace finalization failed: %w", e))
		}
		if err != nil {
			r.Error = err.Error()
		}
		if p.options.JSON {
			out := a.Out
			if out == nil {
				out = io.Discard
			}
			err = errors.Join(err, json.NewEncoder(out).Encode(r))
			return
		}
		if !planWritten {
			err = errors.Join(err, writePlan())
		}
		if planOpen {
			_, e := fmt.Fprintln(diagnostics)
			err = errors.Join(err, e)
		}
		for _, row := range []struct {
			name  string
			stage WorkspaceLiftStage
		}{{"Eval", r.Eval}, {"Push", r.Push}, {"Lift", r.Lift}} {
			_, e := fmt.Fprintf(diagnostics, "%s · %s · %s\n", row.name, row.stage.Status, row.stage.Detail)
			err = errors.Join(err, e)
		}
	}()
	if !p.options.PlanOnly && a.Cfg != nil && a.Run != nil {
		p.kube, err = a.orkaCapture(ctx, nil, "config", "view", "-o", "json")
		if err != nil {
			return r, err
		}
		kube, e := guard.ParseKubeconfig(p.kube)
		if e != nil {
			return r, e
		}
		for i, name := range []string{p.plan.EvalContext, p.plan.DestinationContext} {
			posture, e := guard.Classify(kube, name)
			if e != nil {
				return r, e
			}
			if i == 0 {
				r.Plan.EvalServer = posture.Host
			} else {
				r.Plan.DestinationServer = posture.Host
			}
		}
	}
	if p.options.PlanOnly {
		if !p.options.JSON {
			err = writePlan()
		}
		return r, err
	}
	if a.Cfg == nil || a.Run == nil {
		return r, errors.New("workspace Lift requires operational configuration")
	}
	worker := *a
	cfg := *a.Cfg
	worker.Cfg = &cfg
	worker.Out = io.Discard
	worker.Err = io.Discard
	if p.options.Verbose {
		worker.Out = diagnostics
		worker.Err = diagnostics
	}
	eval := worker.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: p.options.Evaluation.ToContext})
	dest := worker.releaseLiftWorker(ctx, p.options.Destination)
	checkSource := func() error {
		if err := p.store.unchanged(); err != nil {
			return err
		}
		return p.review.checkCurrent(ctx)
	}
	if err = checkSource(); err != nil {
		return r, err
	}
	receipt, _, lookupErr := p.store.latest(ctx, p.review.suite)
	if lookupErr != nil && !errors.Is(lookupErr, errSuitePassMissing) {
		return r, lookupErr
	}
	runID := ""
	if receipt != nil {
		runID = receipt.RunID
	}
	if runID == "" {
		runID, err = randomHex(16)
		if err != nil {
			return r, err
		}
	}
	observedEval, err := eval.prepareSuiteNativeCandidate(ctx, p.review.suite, p.options.Evaluation.ToNamespace, strings.TrimPrefix(p.options.Evaluation.Inference, "provider:"), runID)
	if err != nil {
		return r, err
	}
	observedDest, err := dest.prepareSuiteNative(ctx, p.review.suite, p.options.Destination.ToNamespace, strings.TrimPrefix(p.options.Destination.Inference, "provider:"))
	if err != nil {
		return r, err
	}
	cached := receipt != nil && receipt.CandidateCleanup == "complete" && reflect.DeepEqual(receipt.Subject, observedEval.Subject)
	if !cached && receipt != nil {
		runID, err = randomHex(16)
		if err != nil {
			return r, err
		}
		observedEval, err = eval.prepareSuiteNativeCandidate(ctx, p.review.suite, p.options.Evaluation.ToNamespace, strings.TrimPrefix(p.options.Evaluation.Inference, "provider:"), runID)
		if err != nil {
			return r, err
		}
	}
	if _, _, err = dest.releaseLiftGate(ctx, observedEval.Subject, observedDest.Subject, p.plan.Promote); err != nil {
		return r, err
	}
	publication, err := p.liftRecord(ctx, "publication", p.options.Repository)
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, publication.Close()) }()
	placement, err := p.liftRecord(ctx, "placement", p.options.Repository+"\n"+p.options.Destination.ToContext+"\n"+p.options.Destination.ToNamespace+"\n"+p.options.Destination.Inference)
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, placement.Close()) }()
	if placement.record != nil && publication.record == nil {
		return r, errWorkspaceLiftState
	}
	if publication.record != nil {
		if _, err = suiteoras.ReadArtifactRegistry(ctx, p.options.Repository+"@"+p.plan.SuiteDigest, p.options.Destination.PlainHTTP); err != nil {
			return r, errWorkspaceLiftState
		}
		if p.options.Provenance.Require {
			temporary, e := os.MkdirTemp("", "kmx-lift-provenance-*")
			if e != nil {
				return r, e
			}
			// Reuse bounded provenance validation through registry reads only;
			// a historical publication never authorizes attaching or repairing it.
			pulled, e := suiteoras.PullRegistry(ctx, p.options.Repository+"@"+p.plan.SuiteDigest, filepath.Join(temporary, "suite"), p.options.Destination.PlainHTTP)
			cleanupErr := os.RemoveAll(temporary)
			root := p.review.prepared.Descriptor
			if e != nil || cleanupErr != nil || !pulled.ProvenanceChecked || len(pulled.Provenance) == 0 || pulled.Descriptor.Digest != root.Digest || pulled.Descriptor.Size != root.Size || pulled.Descriptor.MediaType != root.MediaType {
				return r, errors.New("required AgentSuite provenance is missing or unverifiable; completed publication was not repaired")
			}
		}
	}
	if placement.record != nil {
		if placement.record.Placement == nil || !reflect.DeepEqual(placement.record.Placement.Subject, observedDest.Subject) {
			return r, errWorkspaceLiftState
		}
		if err = dest.verifySuiteNative(ctx, placement.record.Placement); err != nil {
			return r, errWorkspaceLiftState
		}
	}
	// Known storage constraints must refuse before evaluation/publication spend.
	// A verified placement skip never opens or repairs the provenance store.
	if placement.record == nil {
		if err = dest.preflightReleaseStateCreate(ctx, releaseStateSubject(observedDest.Subject)); err != nil {
			return r, err
		}
		if err = preflightReleaseLiftStorage(ctx); err != nil {
			return r, err
		}
	}
	var missing []string
	if !cached {
		missing = append(missing, "eval")
	}
	if publication.record == nil {
		missing = append(missing, "push")
	}
	if placement.record == nil {
		missing = append(missing, "lift")
	}
	if len(missing) == 0 {
		missing = []string{"verify-only"}
	}
	r.Plan.Disclosure = strings.Join(missing, ",") + "; source-only proof, not destination evaluation"
	if !cached {
		r.Plan.Disclosure += "; fresh Tasks and UID-safe candidate cleanup; runtime/model retention may outlive cleanup"
		if p.plan.Judging {
			r.Plan.Disclosure += "; judging transfers case inputs, answers and rubrics"
		}
	}
	if publication.record == nil {
		r.Plan.Disclosure += "; publication of source/cases"
	}
	if !p.options.JSON || a.Cfg.Confirm == "" && a.Stdin != nil && term.IsTerminal(int(a.Stdin.Fd())) {
		if err = writePlan(); err != nil {
			return r, err
		}
	}
	grant, err := worker.workspaceLiftConsent(ctx, p, observedEval.Subject, observedDest.Subject, r.Plan.Disclosure, diagnostics)
	if err != nil {
		return r, err
	}
	planOpen = false // Cooked terminal input completed the Plan/prompt line.
	worker.liftConsent = grant
	eval.liftConsent = grant
	dest.liftConsent = grant
	boundary := func() error {
		if e := checkSource(); e != nil {
			return e
		}
		for _, s := range []suiteNativeSubject{observedEval.Subject, observedDest.Subject} {
			w := worker.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: s.Target.Context})
			if e := grant.check(ctx, w, CreateOptions{Name: s.physicalMember(), Namespace: s.Target.Namespace}); e != nil {
				return e
			}
		}
		return nil
	}
	if err = boundary(); err != nil {
		return r, err
	}
	if cached {
		r.Eval.Status = "skipped"
		for _, row := range receipt.Cases {
			if row.Verdict == "pass" {
				r.Eval.Passed++
			}
		}
		r.Eval.Detail = fmt.Sprintf("%d/%d runs passed on %s; fresh subject matched; candidate resources cleaned", r.Eval.Passed, r.Eval.Total, p.plan.EvalName)
	} else {
		r.Eval.Status = "failed"
		if err = p.store.reserve(ctx); err != nil {
			return r, err
		}
		output, e := preflightSuitePassingExport(ctx, filepath.Join(p.store.partition, ".pending-receipt"), p.review.root, p.store.cache)
		if e != nil {
			return r, e
		}
		cases := make([]agentruntime.EvaluationCase, len(p.review.inventory))
		for i, f := range p.review.inventory {
			cases[i] = f.Case
		}
		judge, e := newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: p.options.Evaluation.JudgeBaseURL, KeyEnv: p.options.Evaluation.JudgeKeyEnv}, cases, worker.Err)
		if e != nil {
			output.Close()
			return r, e
		}
		ep := &PreparedSuiteWorkspaceEvaluation{prepared: p.review.prepared, suite: p.review.suite, inventory: p.review.inventory, root: p.review.root, options: p.options.Evaluation, output: output, store: p.store, judge: judge, candidateRun: runID, runs: suiteRunSummary{Total: len(p.review.inventory), SuiteRunExecutionSummary: SuiteRunExecutionSummary{NotExecuted: len(p.review.inventory)}}, cleanup: "candidate cleanup not required"}
		err = eval.EvaluateSuiteWorkspace(ctx, ep)
		r.Eval.Passed = ep.runs.Passed
		r.Eval.SuiteRunExecutionSummary = &ep.runs.SuiteRunExecutionSummary
		r.Eval.Detail = fmt.Sprintf("%d/%d runs passed on %s; candidate resources cleaned", r.Eval.Passed, r.Eval.Total, p.plan.EvalName)
		if err != nil {
			r.Eval.Detail = fmt.Sprintf("%s; nothing published; destination unchanged", err)
			r.Eval.Error = err.Error()
			return r, err
		}
		r.Eval.Status = "complete"
		receipt, _, err = p.store.latest(ctx, p.review.suite)
		if err != nil {
			return r, err
		}
	}
	if err = boundary(); err != nil {
		return r, err
	}
	// Prove an edit before checking an occupied older destination. A passing
	// version still cannot publish or adopt that destination through this route.
	if placement.record == nil {
		for _, doc := range []map[string]any{observedDest.bundle.Provider, observedDest.bundle.Agent} {
			if _, err = dest.inspectReleaseLiftResource(ctx, observedDest.Subject, doc, suiteNativeOwnedResource{}); err != nil {
				return r, err
			}
		}
	}
	// Export canonical receipt bytes, never the managed record envelope, for the
	// unchanged release evidence reader. The private temporary name is not proof.
	evidence, err := p.liftEvidence(ctx, receipt)
	if err != nil {
		return r, err
	}
	defer func() { err = errors.Join(err, evidence.root.Remove(evidence.name), evidence.Close()) }()
	if publication.record != nil {
		r.Push.Status = "skipped"
		r.Push.Detail = p.options.Repository + "@" + p.plan.SuiteDigest
	} else {
		var pushOptions suiteoras.PushOptions
		var sourceWarning string
		if p.options.Provenance.Enabled {
			inputs, e := workspace.SourceFiles(ctx, p.review.root, p.review.prepared)
			if e != nil {
				return r, e
			}
			pushOptions, sourceWarning = suitePushOptions(ctx, p.review.root, p.options.Provenance, inputs)
		}
		if err = boundary(); err != nil {
			return r, err
		}
		r.Push.Status = "unknown"
		r.Push.Detail = "publication incomplete/unknown; copied blobs or tag may exist; destination unchanged"
		var pushed suiteoras.PushResult
		pushed, err = suiteoras.PushLayoutRegistry(ctx, p.review.prepared.Path, p.options.Repository+":s-"+strings.TrimPrefix(p.plan.SuiteDigest, "sha256:"), p.options.Destination.PlainHTTP, false, p.review.prepared.Descriptor, pushOptions)
		if err != nil {
			r.Push.Error = "registry publication failed; outcome unknown"
			return r, errors.New(r.Push.Error)
		}
		r.Push.Status = "complete"
		r.Push.Detail = p.options.Repository + "@" + p.plan.SuiteDigest
		if sourceWarning != "" && pushed.Provenance != nil {
			pushed.Warnings = append(pushed.Warnings, sourceWarning)
		}
		for _, warning := range pushed.Warnings {
			r.Push.Detail += "; " + tuiOneLine(warning)
		}
		if err = publication.commit(ctx, nil); err != nil {
			r.Push.Detail += "; publication succeeded, completion persistence incomplete"
			return r, err
		}
	}
	if err = boundary(); err != nil {
		return r, err
	}
	if placement.record != nil {
		if err = dest.verifySuiteNative(ctx, placement.record.Placement); err != nil {
			return r, errWorkspaceLiftState
		}
		r.Lift.Status = "skipped"
		r.Lift.Detail = p.plan.DestinationName + " Ready; recorded placement verified GET-only"
		return r, nil
	}
	opt := p.options.Destination
	opt.Reference = p.options.Repository + "@" + p.plan.SuiteDigest
	opt.Evidence = evidence.path
	opt.EvaluationName = p.plan.EvalName
	r.Lift.Status = "failed"
	release, err := worker.PrepareReleaseLift(ctx, opt)
	if err != nil {
		r.Lift.Error = err.Error()
		return r, err
	}
	if err = boundary(); err != nil {
		return r, err
	}
	result, err := worker.ApplyReleaseLift(ctx, release, release.summary.PlanDigest)
	r.Placement = &result
	if err != nil {
		r.Lift.Detail = "placement " + result.State + "; inspect resource outcomes"
		r.Lift.Error = err.Error()
		return r, err
	}
	r.Lift.Status = "complete"
	r.Lift.Detail = p.plan.DestinationName + " Ready; passed on " + p.plan.EvalName + ", not evaluated on " + p.plan.DestinationName
	if err = placement.commit(ctx, result.placement); err != nil {
		r.Lift.Detail += "; placement succeeded, completion persistence incomplete"
	}
	return r, err
}
