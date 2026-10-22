package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const flowCandidateMarker = "kaimahi.dev/suite-flow-candidate"
const flowCandidateVersion = "kmx.flow-candidate/v1alpha1"

var flowCandidateID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type suiteFlowCandidateProjection struct {
	Version   string `json:"version"`
	Suite     string `json:"suite"`
	Member    string `json:"member"`
	Case      string `json:"case"`
	Run       int    `json:"run"`
	Step      string `json:"step"`
	Operation string `json:"operation"`
}

func suiteFlowCandidateName(suite, member, caseID string, run int, step, operation string) (string, error) {
	p := suiteFlowCandidateProjection{flowCandidateVersion, suite, member, caseID, run, step, operation}
	if !suitePrefixedDigest(suite) || scaffold.ValidateObjectName(member) != nil || !flowCandidateID.MatchString(caseID) || run < 1 || run > 1000 || step != "" && !flowCandidateID.MatchString(step) || !evaluationRunID(operation) {
		return "", errSuiteFlow
	}
	raw, err := suitecore.Marshal(p)
	if err != nil {
		return "", errSuiteFlow
	}
	sum := sha256.Sum256(raw)
	return "kmx-flow-" + hex.EncodeToString(sum[:24]), nil
}
func (p *suiteFlowCandidateProjection) physicalName() (string, error) {
	if p == nil || p.Version != flowCandidateVersion {
		return "", errSuiteFlow
	}
	return suiteFlowCandidateName(p.Suite, p.Member, p.Case, p.Run, p.Step, p.Operation)
}
func flowCandidateOwner(markers map[string]any) (*suiteFlowCandidateProjection, error) {
	raw, present := markers[flowCandidateMarker]
	if !present {
		return nil, nil
	}
	text, ok := raw.(string)
	if !ok || len(text) > 1024 {
		return nil, errSuiteFlow
	}
	var p suiteFlowCandidateProjection
	if suitecore.Unmarshal([]byte(text), &p) != nil {
		return nil, errSuiteFlow
	}
	canonical, err := suitecore.Marshal(p)
	if err != nil || string(canonical) != text {
		return nil, errSuiteFlow
	}
	if _, err = p.physicalName(); err != nil {
		return nil, errSuiteFlow
	}
	return &p, nil
}
func (o *orkaSuiteOwnership) candidateName() (string, error) {
	if o.FlowCandidate != nil {
		return o.FlowCandidate.physicalName()
	}
	return suiteCandidatePhysicalName(o.Digest, o.EvaluationRun)
}
func sameFlowOperationOwner(a, b *orkaSuiteOwnership) bool {
	if a.FlowCandidate == nil || b.FlowCandidate == nil {
		return a.FlowCandidate == nil && b.FlowCandidate == nil
	}
	return a.Suite == b.Suite && a.Digest == b.Digest && a.Renderer == b.Renderer && a.BindingDigest == b.BindingDigest && a.FlowCandidate.Version == b.FlowCandidate.Version && a.EvaluationRun == b.EvaluationRun
}

// prepareCandidate renders only a registered tuple from this frozen selection.
// It grants no placement, legacy receipt, or runtime dispatch authority.
func (s *suiteFlowSelection) prepareCandidate(ctx context.Context, a *App, p suiteFlowCandidateProjection) (prepared *suiteNativePrepared, err error) {
	defer func() {
		if ctx.Err() != nil {
			prepared = nil
			err = ctx.Err()
		}
	}()
	name, err := p.physicalName()
	if err != nil {
		return nil, err
	}
	known := false
	for _, candidate := range s.candidates {
		if candidate == p {
			known = true
		}
	}
	if !known {
		return nil, errSuiteFlow
	}
	if err := s.revalidate(ctx, a); err != nil {
		return nil, err
	}
	crds := map[string][]byte{}
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		raw, err := a.orkaCapture(ctx, nil, "get", "crd", orkaPlural(kind), "-o", "json")
		if err != nil {
			return nil, errSuiteFlow
		}
		crds[kind] = raw
	}
	validator, err := a.installedOrkaSchema(crds)
	if err != nil {
		return nil, errSuiteFlow
	}
	if err := s.revalidate(ctx, a); err != nil {
		return nil, err
	}
	for _, m := range s.members {
		if m.suite.MemberID == p.Member {
			v := m.subject
			return renderSuiteNativeProjection(&m.suite, s.target.Namespace, name, name, p.Operation, v.Target, v.Binding, v.Runtime, validator, &p, s.bindingDigest)
		}
	}
	return nil, errSuiteFlow
}

func (s *suiteFlowSelection) prepareCandidates() error {
	s.candidates = nil
	names := map[string]bool{}
	add := func(member, caseID string, run int, step string) error {
		p := suiteFlowCandidateProjection{flowCandidateVersion, s.suite, member, caseID, run, step, s.operation}
		name, err := p.physicalName()
		if err != nil || names[name] {
			return errSuiteFlow
		}
		for _, m := range s.members {
			if name == m.subject.Binding.Provider.Name {
				return errSuiteFlow
			}
		}
		names[name] = true
		s.candidates = append(s.candidates, p)
		return nil
	}
	for _, m := range s.members {
		for _, file := range m.suite.Cases {
			c, err := workspace.DecodePackagedCase(file.Bytes)
			if err != nil {
				return errSuiteFlow
			}
			runs, _ := c.RunPolicy()
			for run := 1; run <= runs; run++ {
				if err := add(m.suite.MemberID, c.ID, run, ""); err != nil {
					return err
				}
			}
		}
	}
	for _, c := range s.flow.Cases {
		runs := 1
		if c.Runs != nil {
			runs = *c.Runs
		}
		for run := 1; run <= runs; run++ {
			for _, step := range s.flow.Steps {
				if err := add(step.Member, c.ID, run, step.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
