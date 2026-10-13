package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	godigest "github.com/opencontainers/go-digest"
)

var ErrReleaseLiftApply = errors.New("release lift apply refused or incomplete; inspect resource outcomes; no automatic retry or cleanup")

type ReleaseLiftOutcome struct {
	ReleaseLiftResource
	Outcome string `json:"outcome"`
	Ready   bool   `json:"ready"`
}

// ReleaseLiftResult is a placement observation, never destination evaluation.
// A partial result preserves acknowledged identities without granting reuse.
type ReleaseLiftResult struct {
	Summary   ReleaseLiftSummary   `json:"summary"`
	State     string               `json:"state"`
	Resources []ReleaseLiftOutcome `json:"resources"`
	placement *suiteNativePlacement
}

type releaseLiftInputs struct {
	artifact *suiteoras.Artifact
	suite    *workspace.NativeSuite
	receipt  *suitePassingReceipt
	evidence []byte
}

func readReleaseLiftInputs(ctx context.Context, opt ReleaseLiftOptions) (releaseLiftInputs, error) {
	var in releaseLiftInputs
	var err error
	in.receipt, err = readSuitePassingReceipt(ctx, opt.Evidence)
	if err != nil {
		return in, err
	}
	if opt.FromLayout != "" {
		_, digest, _ := strings.Cut(opt.Reference, "@")
		in.artifact, err = suiteoras.ReadArtifactLayout(ctx, opt.FromLayout, digest)
	} else {
		in.artifact, err = suiteoras.ReadArtifactRegistry(ctx, opt.Reference, opt.PlainHTTP)
	}
	if err != nil {
		return in, err
	}
	in.suite, err = workspace.LoadArtifact(ctx, in.artifact)
	if err != nil {
		return in, err
	}
	in.evidence, err = canonicalSuitePassingReceipt(ctx, in.receipt, in.suite)
	return in, err
}

func (a *App) releaseLiftWorker(ctx context.Context, opt ReleaseLiftOptions) *App {
	worker := *a.withRunContext(ctx)
	cfg := *a.Cfg
	cfg.KubeContext, cfg.ContextSource = opt.ToContext, config.SourceFlag
	worker.Cfg = &cfg
	worker.guarded = false
	return &worker
}

func (a *App) recheckReleaseLiftInputs(ctx context.Context, p *PreparedReleaseLift) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := readReleaseLiftInputs(ctx, p.options)
	if err != nil {
		return err
	}
	if godigest.FromBytes(in.evidence).String() != p.summary.EvidenceDigest {
		return ErrReleaseLiftApply
	}
	current, err := a.prepareSuiteNative(ctx, in.suite, p.options.ToNamespace, strings.TrimPrefix(p.options.Inference, "provider:"))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Subject, p.native.Subject) {
		return ErrReleaseLiftApply
	}
	gate, model, err := a.releaseLiftGate(ctx, in.receipt.Subject, current.Subject, p.options.Promote)
	if err != nil {
		return err
	}
	if gate != p.summary.Gate || model != p.summary.ServingModel {
		return ErrReleaseLiftApply
	}
	return a.verifySuiteNativeInputs(ctx, p.native.Subject)
}

func sameReleaseLiftDecision(before, fresh ReleaseLiftResource) bool {
	// Owned status-only RV churn cannot erase evaluated UID/gen/whole-spec proof.
	before.ResourceVersion, fresh.ResourceVersion = "", ""
	return before == fresh
}

func releaseLiftProof(p *PreparedReleaseLift, i int) suiteNativeOwnedResource {
	if p.summary.Gate == "promotion" {
		return suiteNativeOwnedResource{}
	}
	if i == 0 {
		return p.receipt.Provider
	}
	return p.receipt.Agent
}

func (a *App) checkReleaseLiftLeaves(ctx context.Context, p *PreparedReleaseLift, created []suiteNativeOwnedResource) error {
	for i, doc := range []map[string]any{p.native.bundle.Provider, p.native.bundle.Agent} {
		proof := releaseLiftProof(p, i)
		expected := p.summary.Resources[i]
		if created[i].UID != "" {
			proof = created[i]
			expected.Action, expected.UID, expected.Generation, expected.SpecDigest = "reuse", proof.UID, proof.Generation, proof.SpecDigest
		}
		fresh, err := a.inspectReleaseLiftResource(ctx, p.native.Subject, doc, proof)
		if err != nil {
			return err
		}
		if !sameReleaseLiftDecision(expected, fresh) {
			return ErrReleaseLiftApply
		}
	}
	return nil
}

// ApplyReleaseLift is single-use and consumes the retained review digest. All
// gate/guard refusals precede admission. Only absent-conditional CREATE and exact
// receipt-bound no-write reuse are permitted; eval's update policy is unchanged.
func (a *App) ApplyReleaseLift(ctx context.Context, p *PreparedReleaseLift, reviewedDigest string) (result ReleaseLiftResult, err error) {
	result.State = "not-applied"
	guardFailure := false
	defer func() {
		if err != nil && !guardFailure {
			cause := ctx.Err()
			if cause == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				cause = errContext(err)
			}
			err = errors.Join(ErrReleaseLiftApply, cause)
		}
	}()
	if a == nil || a.Cfg == nil || a.Run == nil || p == nil || p.native == nil || reviewedDigest == "" || reviewedDigest != p.summary.PlanDigest {
		return result, ErrReleaseLiftApply
	}
	result.Summary = p.Summary()
	for _, r := range p.summary.Resources {
		result.Resources = append(result.Resources, ReleaseLiftOutcome{ReleaseLiftResource: r, Outcome: "not-attempted"})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !p.started.CompareAndSwap(false, true) {
		return result, ErrReleaseLiftApply
	}
	worker := a.releaseLiftWorker(ctx, p.options)
	created := make([]suiteNativeOwnedResource, 2)
	recheck := func() error {
		if err := worker.recheckReleaseLiftInputs(ctx, p); err != nil {
			return err
		}
		return worker.checkReleaseLiftLeaves(ctx, p, created)
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := worker.guardOrkaMutation(ctx, CreateOptions{Name: p.summary.Member, Namespace: p.summary.Target.Namespace}, "create absent or reuse exact evaluated suite Provider and Agent in "+p.summary.Target.Namespace); err != nil {
		guardFailure = true
		return result, err
	}
	if err := recheck(); err != nil {
		return result, err
	}
	docs := make([]map[string]any, 0, 2)
	for _, desired := range []map[string]any{p.native.bundle.Provider, p.native.bundle.Agent} {
		doc, err := cloneOrkaDoc(desired)
		if err != nil {
			return result, err
		}
		addOrkaMarker(doc, suiteNativeOwner(p.native.Subject).markers())
		docs = append(docs, doc)
	}
	// Preflight both creations before the first persistent write. Reuse never
	// enters admission, and no Secret skeleton or Task belongs to this path.
	for i, doc := range docs {
		if p.summary.Resources[i].Action != "create" {
			continue
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return result, err
		}
		raw, err := worker.orkaCapture(ctx, body, "-n", p.summary.Target.Namespace, "create", "--dry-run=server", "--validate=strict", "-f", "-", "-o", "json")
		if err != nil {
			return result, err
		}
		if _, err := releaseLiftAdmitted(p.native.Subject, doc, raw, false); err != nil {
			return result, err
		}
	}
	if err := recheck(); err != nil {
		return result, err
	}
	placement := &suiteNativePlacement{Subject: p.native.Subject}
	proofs := []*suiteNativeOwnedResource{&placement.Provider, &placement.Agent}
	for i, doc := range docs {
		if err := recheck(); err != nil {
			return result, err
		}
		row := &result.Resources[i]
		if row.Action == "create" {
			// Set ambiguity before the single write. Failed CREATE never triggers a
			// recovery GET, retry, adoption, rollback or invented acknowledged UID.
			result.State, row.Outcome = "partial", "unknown"
			row.UID, row.Generation, row.ResourceVersion, row.SpecDigest = "", 0, "", ""
			id, raw, err := worker.createOrkaObjectObserved(ctx, p.summary.Target.Namespace, doc)
			if err != nil {
				return result, err
			}
			row.Outcome, row.UID, row.Generation = "created", id.UID, id.Generation
			proof, err := releaseLiftAdmitted(p.native.Subject, doc, raw, true)
			if err != nil {
				return result, err
			}
			created[i] = proof
			row.SpecDigest = proof.SpecDigest
			if err := worker.waitOrkaReady(ctx, p.summary.Target.Namespace, id); err != nil {
				return result, err
			}
		} else {
			row.Outcome = "reused"
		}
		if err := recheck(); err != nil {
			return result, err
		}
		row.Ready = true
		proof := releaseLiftProof(p, i)
		if created[i].UID != "" {
			proof = created[i]
		}
		*proofs[i] = proof
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := worker.verifySuiteNative(ctx, placement); err != nil {
		return result, err
	}
	result.State, result.placement = "ready", placement
	return result, nil
}

func errContext(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return context.DeadlineExceeded
}

// releaseLiftAdmitted permits server metadata but no unreviewed spec mutation.
// UID/gen is credited only to an acknowledged real create, never a dry-run UID.
func releaseLiftAdmitted(subject suiteNativeSubject, desired map[string]any, raw []byte, real bool) (suiteNativeOwnedResource, error) {
	var proof suiteNativeOwnedResource
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || doc["kind"] != desired["kind"] || doc["apiVersion"] != desired["apiVersion"] {
		return proof, ErrReleaseLiftApply
	}
	meta, ok := doc["metadata"].(map[string]any)
	if !ok || meta["name"] != subject.Member || meta["namespace"] != subject.Target.Namespace || meta["deletionTimestamp"] != nil {
		return proof, ErrReleaseLiftApply
	}
	ann, _ := meta["annotations"].(map[string]any)
	if !reflect.DeepEqual(suiteNativeOwner(subject).markers(), suiteOnlyMarkers(ann)) || !orkaRenderedMetadataEqual(meta, desired["metadata"].(map[string]any)) || !reflect.DeepEqual(doc["spec"], desired["spec"]) {
		return proof, ErrReleaseLiftApply
	}
	if !real {
		return proof, nil
	}
	_, id, err := inspectSuiteNativeLive(raw, subject.Target.Namespace, desired["kind"].(string), subject.Member, suiteNativeOwner(subject))
	if err != nil {
		return proof, err
	}
	digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	if err != nil {
		return proof, err
	}
	return suiteNativeOwnedResource{UID: id.UID, Generation: id.Generation, SpecDigest: digest}, nil
}

// VerifyReleaseLiftPlacement revalidates an opaque in-process success result
// before invocation. Serialized summaries cannot manufacture placement proof.
func (a *App) VerifyReleaseLiftPlacement(ctx context.Context, result ReleaseLiftResult) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ErrReleaseLiftApply, ctx.Err())
		}
	}()
	if a == nil || a.Cfg == nil || a.Run == nil || result.State != "ready" || result.placement == nil {
		return ErrReleaseLiftApply
	}
	worker := a.releaseLiftWorker(ctx, ReleaseLiftOptions{ToContext: result.placement.Subject.Target.Context})
	return worker.verifySuiteNative(ctx, result.placement)
}
