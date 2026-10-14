package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/imagelift"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/opencontainers/go-digest"
)

// EvaluateImageRelease evaluates a complete immutable image release. Placement
// here is evaluation setup, not evidence-gated promotion; no native receipts are
// reused. Every request is independent and never automatically resent.
func (a *App) EvaluateImageRelease(ctx context.Context, opt ReleaseLiftOptions, output string) error {
	return a.evaluateImageRelease(ctx, opt, output, func(ctx context.Context, worker *App, record WorkspaceDeployment, member, prompt string) (string, error) {
		return worker.RunSuiteDeployment(ctx, record, member, prompt)
	})
}

func (a *App) evaluateImageRelease(ctx context.Context, opt ReleaseLiftOptions, output string, invoke func(context.Context, *App, WorkspaceDeployment, string, string) (string, error)) error {
	if opt.Environment == "" || opt.Inference != "" || opt.Bindings != "" || opt.Promote {
		return errSuitePassingReceipt
	}
	x, err := a.prepareImageCandidate(ctx, opt)
	if err != nil {
		return err
	}
	inventory, casesDigest, err := imageInventory(x.suite)
	if err != nil {
		return err
	}
	state, err := config.StateDir()
	if err != nil {
		return err
	}
	export, err := preflightSuitePassingExport(ctx, output, filepath.Join(state, "suites"), filepath.Join(state, "suite-images"))
	if err != nil {
		return err
	}
	defer export.Close()
	worker := a.releaseLiftWorker(ctx, opt)
	// Do not let evaluation adopt or update pre-existing names. A later eval
	// uses an explicitly selected fresh evaluation placement.
	for _, doc := range x.plan.Objects {
		row, err := worker.inspectImageReleaseResource(ctx, x, doc, nil)
		if err != nil {
			return err
		}
		if row.Action != "create" {
			return errors.New("image evaluation requires absent placement names")
		}
	}
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: x.plan.Name, Namespace: x.env.Namespace}, "place image suite and execute its full evaluation inventory"); err != nil {
		return err
	}
	fresh, err := a.prepareImageCandidate(ctx, opt)
	if err != nil || !reflect.DeepEqual(fresh.subject, x.subject) {
		return ErrReleaseLift
	}
	placement, err := imagelift.Deploy(ctx, imageEvaluationCluster{imageLiftCluster{app: worker}}, x.plan, func() error { return nil })
	if err != nil {
		return err
	}
	runID, err := randomHex(16)
	if err != nil {
		return err
	}
	r := &suiteImagePassingReceipt{SchemaVersion: 1, Domain: "kmx.image-http.passing/v1", RunID: runID, Subject: x.subject, CasesDigest: casesDigest, Result: "pass"}
	for _, doc := range x.plan.Objects {
		row, err := worker.inspectImageReleaseResource(ctx, x, doc, nil)
		if err != nil {
			return err
		}
		r.Resources = append(r.Resources, row)
	}
	record := WorkspaceDeployment{WorkspaceName: x.suite.Name, Environment: x.env}
	record.Receipt.Plan = rt.SuitePlanSummary{ArtifactDigest: x.suite.Digest, Target: rt.SuiteTarget{Context: x.env.Context, ClusterUID: x.env.ClusterUID, Namespace: x.env.Namespace}, Members: []rt.SuiteMemberSummary{{Agent: x.suite.MemberID, Digest: x.plan.Digest, ConfigurationDigest: x.subject.ConfigurationDigest[len("sha256:"):]}}}
	member := rt.SuiteMemberOutcome{Agent: x.suite.MemberID, State: "ready"}
	for _, resource := range placement.Resources {
		member.Resources = append(member.Resources, rt.ResourceResult{Kind: resource.Kind, Name: resource.Name, UID: resource.UID, Namespace: x.env.Namespace})
	}
	record.Receipt.Members = []rt.SuiteMemberOutcome{member}
	check := func() error {
		current, err := a.prepareImageCandidate(ctx, opt)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.subject, x.subject) {
			return errors.New("image evaluation bindings changed")
		}
		for i, doc := range x.plan.Objects {
			if _, err := worker.inspectImageReleaseResource(ctx, x, doc, &r.Resources[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range inventory {
		if err := check(); err != nil {
			return err
		}
		requestID, err := randomHex(16)
		if err != nil {
			return err
		}
		caseCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		answer, err := invoke(caseCtx, worker, record, x.suite.MemberID, c.Case.Input)
		cancel()
		if err != nil {
			return errors.New("image evaluation request failed or outcome unknown; no passing evidence exported")
		}
		if err := check(); err != nil {
			return err
		}
		assertions := rt.EvaluateAssertions(c.Case, rt.EvaluationEvidence{Answer: answer, AnswerAvailable: true})
		if rt.EvaluationAssertionsVerdict(assertions) != rt.EvaluationPass {
			return errors.New("image evaluation case failed; no passing evidence exported")
		}
		row := suiteImagePassingCase{ID: c.Metadata.ID, CaseDigest: c.Metadata.CaseDigest, InputDigest: c.Metadata.InputDigest, RequestID: requestID, DeploymentUID: r.Resources[1].UID, AnswerDigest: rt.EvaluationAnswerDigest(answer), Assertions: c.Metadata.Assertions}
		r.Cases = append(r.Cases, row)
	}
	if err := check(); err != nil {
		return err
	}
	raw, err := canonicalImagePassing(ctx, r, x.suite)
	if err != nil {
		return err
	}
	return export.publishBytes(ctx, raw)
}

type imageEvaluationCluster struct{ imageLiftCluster }

func (c imageEvaluationCluster) Call(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "replace") {
		return nil, errors.New("image evaluation cannot adopt or replace occupied resources")
	}
	return c.imageLiftCluster.Call(ctx, input, args...)
}

func (a *App) consoleImageWorkspaceLift(ctx context.Context, request consoleWorkspaceLiftRequest) agentTUICreateResult {
	fail := func(err error) agentTUICreateResult { return agentTUICreateResult{err: err} }
	checkBindings := func() error {
		raw, err := os.ReadFile(request.provider)
		if err != nil || len(raw) > 64<<10 || digest.FromBytes(raw).String() != request.imageBindingsDigest {
			return errors.New("image bindings changed since consent; review again")
		}
		return nil
	}
	if err := checkBindings(); err != nil {
		return fail(err)
	}
	if err := request.review.checkCurrent(ctx); err != nil {
		return fail(err)
	}
	worker := *request.env.app(a).withRunContext(ctx)
	cfg := *worker.Cfg
	cfg.Confirm = request.typedContext
	worker.Cfg = &cfg
	worker.Out, worker.Err, worker.Stdin = io.Discard, io.Discard, nil
	server, err := worker.consoleCreateTarget(ctx, request.env)
	if err != nil || server != request.server {
		return fail(ErrReleaseLift)
	}
	if request.prepared == nil {
		if request.imageOptions.Registry == "" {
			return fail(errors.New("image review requires console --registry"))
		}
		pub, err := worker.PublishSuiteWorkspace(ctx, request.review.root, request.review.image.Name, request.imageOptions.Registry, request.imageOptions.PlainHTTP, request.imageOptions.Build)
		if err != nil {
			return fail(err)
		}
		_, publishedDigest, _ := strings.Cut(pub.Suite, "@")
		if publishedDigest != request.review.digest() {
			return fail(errors.New("published candidate differs from source review; no evaluation dispatched"))
		}
		if err := request.review.checkCurrent(ctx); err != nil {
			return fail(err)
		}
		base, err := privateWorkspaceState("image-evaluation")
		if err != nil {
			return fail(err)
		}
		run, err := os.MkdirTemp(base, "run-")
		if err != nil {
			return fail(err)
		}
		evidence := filepath.Join(run, "passing.json")
		opt := ReleaseLiftOptions{Reference: pub.Release, ToContext: request.env.Name, ToNamespace: request.namespace, Environment: request.provider, Evidence: evidence, PlainHTTP: request.imageOptions.PlainHTTP}
		if err := checkBindings(); err != nil {
			return fail(err)
		}
		if err := worker.EvaluateImageRelease(ctx, opt, evidence); err != nil {
			return fail(err)
		}
		if err := request.review.checkCurrent(ctx); err != nil {
			return fail(err)
		}
		plan, err := worker.PrepareReleaseLift(ctx, opt)
		if err != nil {
			return fail(err)
		}
		return agentTUICreateResult{prepared: &preparedQuickstartSuite{Plan: plan, Workspace: request.review.root, Evidence: evidence}}
	}
	s := request.prepared.Plan.Summary()
	if s.Runtime != "image-http" || s.SuiteDigest != request.review.digest() || s.Target.Context != request.env.Name || s.Target.Namespace != request.namespace {
		return fail(ErrReleaseLift)
	}
	result, err := worker.applyWorkspaceSuiteLift(ctx, request.prepared, request.reviewedDigest)
	if err == nil {
		err = worker.VerifyReleaseLiftPlacement(ctx, result)
	}
	return agentTUICreateResult{result: result, verified: err == nil, err: err}
}
