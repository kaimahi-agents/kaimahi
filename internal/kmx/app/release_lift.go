package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	godigest "github.com/opencontainers/go-digest"
)

var ErrReleaseLift = errors.New("release lift planning refused; no native admission or placement")

// ReleaseLiftOptions selects retrieval, access and evidence, never runtime/model.
// FromLayout uses the digest in Reference; otherwise Reference is a registry ref.
type ReleaseLiftOptions struct {
	Reference, FromLayout, ToContext, ToNamespace, Inference, Evidence, Bindings string
	PlainHTTP, Promote                                                           bool
	// ExpectedRuntime is checked offline against the loaded package before GETs.
	ExpectedRuntime string
}

type ReleaseLiftTarget struct {
	Context      string `json:"context"`
	ClusterUID   string `json:"clusterUID"`
	Namespace    string `json:"namespace"`
	NamespaceUID string `json:"namespaceUID"`
}

type ReleaseLiftResource struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Action          string `json:"action"`
	UID             string `json:"uid,omitempty"`
	Generation      int64  `json:"generation,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	SpecDigest      string `json:"specDigest"`
}

// ReleaseLiftSummary contains review identities, not rendering or execution data.
type ReleaseLiftSummary struct {
	Suite          string                `json:"suite"`
	SuiteDigest    string                `json:"suiteDigest"`
	Member         string                `json:"member"`
	Model          string                `json:"model"`
	Runtime        string                `json:"runtime"`
	Target         ReleaseLiftTarget     `json:"target"`
	SourceTarget   ReleaseLiftTarget     `json:"sourceTarget"`
	Promote        bool                  `json:"promote"`
	ServingModel   string                `json:"servingModel,omitempty"`
	Inference      string                `json:"inference"`
	BindingDigest  string                `json:"bindingDigest"`
	RenderedDigest string                `json:"renderedDigest"`
	EvidenceDigest string                `json:"evidenceDigest"`
	EvidenceRunID  string                `json:"evidenceRunID"`
	Gate           string                `json:"gate"`
	Resources      []ReleaseLiftResource `json:"resources"`
	PlanDigest     string                `json:"planDigest"`
}

// PreparedReleaseLift retains its verified subject/evidence and GET-only review.
// It confers no write authority. Guarded apply and explicit promotion are separate.
type PreparedReleaseLift struct {
	options  ReleaseLiftOptions
	artifact *suiteoras.Artifact
	native   *suiteNativePrepared
	kagent   *releaseLiftKagent
	receipt  *suitePassingReceipt
	summary  ReleaseLiftSummary
	started  atomic.Bool
}

func (p *PreparedReleaseLift) Summary() ReleaseLiftSummary {
	out := p.summary
	out.Resources = slices.Clone(out.Resources)
	return out
}

// PrepareReleaseLift is the deploy-only planning entry point. It performs bounded
// artifact retrieval and GETs, not server dry-run/admission, Tasks or placement.
// Matching unsigned operator-selected observations do not authenticate a signer.
func (a *App) PrepareReleaseLift(ctx context.Context, opt ReleaseLiftOptions) (plan *PreparedReleaseLift, err error) {
	defer func() {
		if err != nil {
			cause := ctx.Err()
			if cause == nil {
				for _, c := range []error{context.Canceled, context.DeadlineExceeded} {
					if errors.Is(err, c) {
						cause = c
						break
					}
				}
			}
			if errors.Is(err, errSuiteKagentOccupied) {
				cause = errors.Join(cause, errSuiteKagentOccupied)
			}
			if errors.Is(err, workspace.ErrMultiMemberUnsupported) {
				cause = errors.Join(cause, workspace.ErrMultiMemberUnsupported)
			}
			err = errors.Join(ErrReleaseLift, cause)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.Cfg == nil || a.Run == nil || ValidateReleaseLiftOptions(opt) != nil {
		return nil, ErrReleaseLift
	}
	provider := strings.TrimPrefix(opt.Inference, "provider:")
	inputs, err := readReleaseLiftInputs(ctx, opt)
	if err != nil {
		return nil, err
	}
	artifact, suite, receipt, evidence := inputs.artifact, inputs.suite, inputs.receipt, inputs.evidence
	if opt.ExpectedRuntime != "" && opt.ExpectedRuntime != suite.Runtime.ID {
		return nil, ErrReleaseLift
	}
	// Closed packaged runtime and canonical E select the only allowed selector
	// before any cluster observation. Evidence never supplies an Orka alias.
	worker := a.releaseLiftWorker(ctx, opt)
	if suite.Runtime.ID == "kagent" {
		if opt.Bindings == "" || receipt.ExecutionMode != "native-kagent" {
			return nil, ErrReleaseLift
		}
		return worker.prepareReleaseLiftKagent(ctx, opt, inputs)
	}
	if opt.Bindings != "" || receipt.ExecutionMode != "native-orka" {
		return nil, ErrReleaseLift
	}
	if opt.ToNamespace == "" {
		opt.ToNamespace = OrkaNamespace
	}
	native, err := worker.prepareSuiteNative(ctx, suite, opt.ToNamespace, provider)
	if err != nil {
		return nil, err
	}
	gate, servingModel, err := worker.releaseLiftGate(ctx, receipt.Subject, native.Subject, opt.Promote)
	if err != nil {
		return nil, err
	}
	s := ReleaseLiftSummary{Suite: suite.Name, SuiteDigest: suite.Digest, Member: suite.MemberID, Model: suite.Model, Runtime: suite.Runtime.ID, Target: releaseLiftTarget(native.Subject.Target), SourceTarget: releaseLiftTarget(receipt.Subject.Target), Promote: opt.Promote, ServingModel: servingModel, Inference: opt.Inference, BindingDigest: native.Subject.BindingDigest, RenderedDigest: native.Subject.RenderedDigest, EvidenceDigest: godigest.FromBytes(evidence).String(), EvidenceRunID: receipt.RunID, Gate: gate}
	for i, doc := range []map[string]any{native.bundle.Provider, native.bundle.Agent} {
		proof := []suiteNativeOwnedResource{receipt.Provider, receipt.Agent}[i]
		if gate == "promotion" {
			// Source execution UIDs cannot authorize existing destination objects.
			proof = suiteNativeOwnedResource{}
		}
		resource, err := worker.inspectReleaseLiftResource(ctx, native.Subject, doc, proof)
		if err != nil {
			return nil, err
		}
		s.Resources = append(s.Resources, resource)
	}
	// Reject spliced/different prerequisites even during this read-only review.
	if err := worker.verifySuiteNativeInputs(ctx, native.Subject); err != nil {
		return nil, err
	}
	// Receipt validation already classifies its random run ID. The native
	// subject has no run ID and its generic key-shape hash guard cannot be used
	// for this typed plan without misclassifying valid evidence identities.
	review, err := spec.Marshal(struct {
		Domain string
		Plan   ReleaseLiftSummary
	}{"kmx.release-lift.plan/v1", s})
	if err != nil {
		return nil, err
	}
	s.PlanDigest = godigest.FromBytes(review).String()
	return &PreparedReleaseLift{options: opt, artifact: artifact, native: native, receipt: receipt, summary: s}, nil
}

// ValidateReleaseLiftOptions checks selectors without config, retrieval or I/O.
func ValidateReleaseLiftOptions(opt ReleaseLiftOptions) error {
	ref, digest, pinned := strings.Cut(opt.Reference, "@")
	provider := strings.TrimPrefix(opt.Inference, "provider:")
	if !pinned || !verificationIdentity(ref) || !spec.IsValidDigest(digest) ||
		!verificationIdentity(opt.ToContext) || scaffold.RefuseKeyShapes(opt.ToContext) != nil ||
		(opt.Inference == "") == (opt.Bindings == "") ||
		opt.Inference != "" && (!strings.HasPrefix(opt.Inference, "provider:") || scaffold.ValidateObjectName(provider) != nil) ||
		opt.Bindings != "" && strings.TrimSpace(opt.Bindings) == "" ||
		opt.ToNamespace != "" && scaffold.ValidateNamespace(opt.ToNamespace) != nil || strings.TrimSpace(opt.Evidence) == "" || opt.FromLayout != "" && opt.PlainHTTP {
		return ErrReleaseLift
	}
	return nil
}

func releaseLiftTarget(t suiteNativeTarget) ReleaseLiftTarget {
	return ReleaseLiftTarget(t)
}

func (a *App) inspectReleaseLiftResource(ctx context.Context, subject suiteNativeSubject, desired map[string]any, proof suiteNativeOwnedResource) (ReleaseLiftResource, error) {
	kind := desired["kind"].(string)
	name := subject.Member
	out := ReleaseLiftResource{Kind: kind, Name: name, Action: "create"}
	raw, err := a.orkaCapture(ctx, nil, "-n", subject.Target.Namespace, "get", orkaPlural(kind), name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return out, err
	}
	if len(raw) == 0 {
		out.SpecDigest, err = suiteNativeDigest("kmx.native-orka.spec/v1", desired["spec"])
		return out, err
	}
	owner := suiteNativeOwner(subject)
	doc, id, err := inspectSuiteNativeLive(raw, subject.Target.Namespace, kind, name, owner)
	if err != nil {
		return out, err
	}
	meta := doc["metadata"].(map[string]any)
	digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	if err != nil {
		return out, err
	}
	// Whole admitted spec plus UID/generation is the original evaluated proof.
	// Do not normalize it by dry-run or silently take fresh proof for a replacement.
	if id.UID != proof.UID || id.Generation != proof.Generation || digest != proof.SpecDigest ||
		!reflect.DeepEqual(owner.markers(), suiteOnlyMarkers(meta["annotations"].(map[string]any))) ||
		!orkaRenderedMetadataEqual(meta, desired["metadata"].(map[string]any)) {
		return out, ErrReleaseLift
	}
	var observed orkaObject
	if json.Unmarshal(raw, &observed) != nil || !orkaCurrentGenerationReady(&observed, id.Generation) {
		return out, ErrReleaseLift
	}
	out.Action, out.UID, out.Generation, out.SpecDigest = "reuse", id.UID, id.Generation, digest
	out.ResourceVersion, _ = meta["resourceVersion"].(string)
	return out, nil
}
