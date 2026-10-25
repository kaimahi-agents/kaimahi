package app

import (
	"context"
	"encoding/json"
	"errors"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	godigest "github.com/opencontainers/go-digest"
)

// ErrAgentSessionsLiftPlanOnly refuses apply: AgentSessions deployment renders a
// substrate ActorTemplate and harness registration for review and writes
// nothing. Substrate apply is a separate, unimplemented seam.
var ErrAgentSessionsLiftPlanOnly = errors.New("AgentSessions deployment is plan-only: it renders an ActorTemplate and harness registration and applies nothing")

var errAgentSessionsLiftSelectors = errors.New("packaged AgentSessions deployment requires --to NAME declaring runtime: agentsessions and an explicit namespace (no --inference or --bindings); alternatively use the offline --harness-image NAME@sha256:<digest> --bindings FILE --plan render without cluster selectors")

// ReleaseLiftSessionsPlan is the rendered AgentSessions half of a plan. It
// contains identities, references and the rendered documents, never prompts,
// instructions or credential values.
type ReleaseLiftSessionsPlan struct {
	agentsessions.Rendered
	// EvidenceBindingDigest is the binding the evaluation recorded. It may name
	// another environment; both bindings serve the suite's pinned model.
	EvidenceBindingDigest string `json:"evidenceBindingDigest"`
	// ConfigDigest binds the exact chat config every Session on this harness
	// must send: the packaged instructions as system_prompt.
	ConfigDigest string `json:"configDigest"`
}

// releaseLiftSessionsRefusal marks a binding, model-match or render refusal
// whose message names fields and validated identities (model, member), never
// credential values, so callers may show it.
type releaseLiftSessionsRefusal struct{ err error }

func (e *releaseLiftSessionsRefusal) Error() string { return e.err.Error() }
func (e *releaseLiftSessionsRefusal) Unwrap() error { return e.err }

type releaseLiftSessions struct {
	plan []byte
}

// PrepareAgentSessionsReleaseLift renders a plan for a packaged AgentSessions
// suite from full-passing AgentSessions evidence, an AgentSessionsBinding and
// a digest-pinned harnessnode image. It reads the artifact, evidence and
// binding only: no cluster configuration, substrate or host calls.
func PrepareAgentSessionsReleaseLift(ctx context.Context, opt ReleaseLiftOptions) (plan *PreparedReleaseLift, err error) {
	defer func() {
		if err != nil {
			// The refusal stays wrapped so App.PrepareReleaseLift, which
			// delegates here, can show it too.
			cause := ctx.Err()
			var refusal *releaseLiftSessionsRefusal
			if errors.As(err, &refusal) {
				cause = errors.Join(cause, refusal)
			}
			err = errors.Join(ErrReleaseLift, cause)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opt.HarnessImage == "" || ValidateReleaseLiftOptions(opt) != nil {
		return nil, ErrReleaseLift
	}
	in, err := readReleaseLiftInputs(ctx, opt)
	if errors.Is(err, errSuiteSessionsReceiptV1) {
		return nil, &releaseLiftSessionsRefusal{errSuiteSessionsReceiptV1}
	}
	if err != nil {
		return nil, err
	}
	suite, receipt := in.suite, in.receipt
	if opt.ExpectedRuntime != "" && opt.ExpectedRuntime != suite.Runtime.ID {
		return nil, ErrReleaseLift
	}
	if suite.Runtime.ID != "agentsessions" || receipt.ExecutionMode != "native-agentsessions" || receipt.Sessions == nil {
		return nil, ErrReleaseLift
	}
	// Receipt coherence already bound the evidence subject to this suite; the
	// evidence model is the suite's pinned model.
	if receipt.Sessions.Subject.Model != suite.Model {
		return nil, ErrReleaseLift
	}
	binding, err := snapshotSuiteSessionsBinding(opt.Bindings, suite)
	if err != nil {
		return nil, &releaseLiftSessionsRefusal{err}
	}
	rendered, err := agentsessions.Render(agentsessions.RenderInput{Suite: suite.Name, Member: suite.MemberID, Model: suite.Model, HarnessImage: opt.HarnessImage, Binding: binding.binding})
	if err != nil {
		return nil, &releaseLiftSessionsRefusal{err}
	}
	config, err := agentsessions.ConfigDigest(string(suite.Instructions))
	if err != nil {
		return nil, ErrReleaseLift
	}
	sessions := &ReleaseLiftSessionsPlan{Rendered: *rendered, EvidenceBindingDigest: receipt.Sessions.Subject.BindingDigest, ConfigDigest: config}
	renderedBytes, err := suitecore.Marshal(struct {
		Domain   string
		Rendered agentsessions.Rendered
	}{"kmx.agentsessions.release-lift.render/v1", *rendered})
	if err != nil {
		return nil, err
	}
	s := ReleaseLiftSummary{
		Suite: suite.Name, SuiteDigest: suite.Digest, Member: suite.MemberID, Model: suite.Model, Runtime: suite.Runtime.ID,
		BindingDigest: rendered.BindingDigest, RenderedDigest: godigest.FromBytes(renderedBytes).String(),
		EvidenceDigest: godigest.FromBytes(in.evidence).String(), EvidenceRunID: receipt.RunID, Gate: "plan-only",
		Resources: []ReleaseLiftResource{
			{Kind: "ActorTemplate", Name: rendered.ActorTemplate.Metadata.Name, Action: "render", SpecDigest: rendered.TemplateDigest},
			{Kind: "HarnessRegistration", Name: suite.MemberID, Action: "render", SpecDigest: rendered.RegistrationSpecDigest},
		},
		AgentSessions: sessions,
	}
	review, err := suitecore.Marshal(struct {
		Domain string
		Plan   ReleaseLiftSummary
	}{"kmx.release-lift.plan/v1", s})
	if err != nil {
		return nil, err
	}
	s.PlanDigest = godigest.FromBytes(review).String()
	retained, err := json.Marshal(sessions)
	if err != nil {
		return nil, err
	}
	s.AgentSessions = nil
	return &PreparedReleaseLift{options: opt, artifact: in.artifact, receipt: receipt, summary: s, sessions: &releaseLiftSessions{plan: retained}}, nil
}

// summary returns a fresh copy so callers cannot mutate the retained render.
func (s *releaseLiftSessions) summary() *ReleaseLiftSessionsPlan {
	if s == nil {
		return nil
	}
	var out ReleaseLiftSessionsPlan
	if json.Unmarshal(s.plan, &out) != nil {
		return nil
	}
	return &out
}
