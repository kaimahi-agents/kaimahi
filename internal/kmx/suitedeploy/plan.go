package suitedeploy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

// Document is a native resource or API-registration request. ID must be unique
// across the whole plan. Apply distinguishes mutations from review-only data.
type Document struct {
	ID    string
	Apply bool
	Bytes []byte
}

type MemberSummary struct {
	Agent         string
	Kind          string
	Image         *agentsuite.Descriptor
	Composition   *agentsuite.Descriptor
	Execution     *agentsuite.Descriptor
	BindingDigest string
	Mode          string
}

type PlanSummary struct {
	OperationID   string
	Instance      string
	SuiteManifest agentsuite.Descriptor
	LogicalDigest string
	Target        Target
	Installation  Installation
	Reconcile     bool
	Members       []MemberSummary
	Digest        string
}

// Plan freezes native bytes and selection identities between review and Deploy.
// It is in-process data, not a persisted or portable Kubernetes manifest format.
type Plan struct {
	summary   PlanSummary
	documents []Document
}

func NewPlan(request Request, installation Installation, documents []Document) (*Plan, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if installation.Runtime != request.Target.Runtime || installation.Version == "" || installation.Identity == "" || installation.CapabilityRevision == "" {
		return nil, errors.New("installation must identify the selected runtime and capabilities")
	}
	if request.Reconcile && !installation.Reconcile {
		return nil, errors.New("runtime installation does not support reconciliation")
	}
	summary := PlanSummary{OperationID: request.OperationID, Instance: request.Instance, SuiteManifest: request.Suite.Artifact.Descriptor, LogicalDigest: request.Suite.LogicalDigest, Target: request.Target, Installation: clone(installation), Reconcile: request.Reconcile}
	for _, selection := range request.Selections {
		if !contains(installation.BindingFormats, selection.Binding.Format) {
			return nil, errors.New("runtime does not accept member binding format")
		}
		member := MemberSummary{Agent: selection.Agent, Kind: selection.Kind, BindingDigest: selection.Binding.Digest, Mode: selection.Mode}
		if image := selection.Image; image != nil {
			if !contains(installation.ExecutionContracts, image.Execution.Schema) {
				return nil, errors.New("runtime does not accept image execution contract")
			}
			if !contains(installation.HarnessABIs, image.Execution.HarnessABI) {
				return nil, errors.New("runtime does not accept image harness ABI")
			}
			for _, mode := range image.Execution.Modes {
				if mode.Name == selection.Mode && (!contains(installation.Protocols, mode.Protocol) || !slices.Contains(installation.Lifecycles, mode.Lifecycle)) {
					return nil, errors.New("runtime does not support selected protocol/lifecycle")
				}
			}
			for _, required := range image.Execution.RequiredCapabilities {
				if !contains(installation.Capabilities, required) {
					return nil, fmt.Errorf("unsupported image capability %s/%s", required.Name, required.Version)
				}
			}
			member.Image = &image.Image.Descriptor
			member.Composition = &image.Composition
			member.Execution = &image.ExecutionDescriptor
		}
		summary.Members = append(summary.Members, clone(member))
	}
	seen := map[string]bool{}
	apply := false
	for _, doc := range documents {
		if doc.ID == "" || len(doc.Bytes) == 0 || seen[doc.ID] {
			return nil, errors.New("native documents require unique resource IDs and nonempty bytes")
		}
		seen[doc.ID] = true
		apply = apply || doc.Apply
	}
	if !apply {
		return nil, errors.New("suite plan has no deployable documents")
	}
	// Framing includes disposition and ordered bytes, so changing review-only
	// content to a mutation cannot preserve the approved deployment identity.
	data, err := json.Marshal(struct {
		Summary   PlanSummary
		Documents []Document
	}{summary, documents})
	if err != nil {
		return nil, err
	}
	summary.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	return &Plan{summary: summary, documents: clone(documents)}, nil
}

func (p *Plan) Summary() PlanSummary  { return clone(p.summary) }
func (p *Plan) Documents() []Document { return clone(p.documents) }

// Authorization binds both gate evaluation and user/application approval to one
// immutable plan. It is not authentication or authority over the cluster API.
type Authorization struct {
	digest   string
	evidence []string
}

func Authorize(plan *Plan, decision GateDecision, reviewedDigest string) (Authorization, error) {
	if plan == nil || reviewedDigest != plan.summary.Digest || decision.PlanDigest != reviewedDigest {
		return Authorization{}, errors.New("gate and review must identify the exact plan")
	}
	if decision.Verdict != "pass" && decision.Verdict != "not-required" {
		return Authorization{}, errors.New("evaluation gate does not permit deployment")
	}
	for _, digest := range decision.EvidenceDigests {
		if !validDigest(digest) {
			return Authorization{}, errors.New("invalid evaluation evidence digest")
		}
	}
	if decision.Verdict == "pass" && len(decision.EvidenceDigests) == 0 {
		return Authorization{}, errors.New("passing gate requires evidence identity")
	}
	return Authorization{digest: reviewedDigest, evidence: clone(decision.EvidenceDigests)}, nil
}

func (a Authorization) Validate(plan *Plan) error {
	if plan == nil || a.digest == "" || a.digest != plan.summary.Digest {
		return errors.New("deployment authorization does not match plan")
	}
	return nil
}

func contains(values []VersionedContract, want VersionedContract) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func clone[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}
