package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

// SuiteTarget separates placement from the selected agent runtime. Local and
// remote Kubernetes destinations use the same deployment adapter.
type SuiteTarget struct {
	Context    string `json:"context"`
	ClusterUID string `json:"clusterUID"`
	Namespace  string `json:"namespace"`
	Runtime    ID     `json:"runtime"`
}

type SuiteMemberBinding struct {
	Name         string                  `json:"name"`
	ProviderType string                  `json:"providerType"`
	BaseURL      string                  `json:"baseURL"`
	SecretRef    agentsuite.SecretKeyRef `json:"secretRef"`
	Engine       string                  `json:"engine,omitempty"`
	// Image is meaningful only for an image-capable adapter. Native adapters
	// must refuse it rather than silently substitute declarative execution.
	Image string `json:"image,omitempty"`
}

type SuiteDeployRequest struct {
	Suite          *agentsuite.DeploymentSuite
	ArtifactDigest string
	Instance       string
	Target         SuiteTarget
	Bindings       map[string]SuiteMemberBinding
	Reconcile      bool
}

// SuiteMemberPlan is held privately by PreparedSuiteDeployment. The adapter
// carries the exact native payload; public summaries contain identities only.
type SuiteMemberPlan struct {
	ConfigurationDigest string
	Agent               string
	Name                string
	Digest              string
	Resources           []string
	Payload             any
}

type SuiteMemberOutcome struct {
	Agent     string           `json:"agent"`
	State     string           `json:"state"`
	Resources []ResourceResult `json:"resources,omitempty"`
}

// SuiteDeploymentAdapter extends deployment independently of chat discovery.
// Prepare is pure. Inspect checks live installation, binding and resource state
// and returns a fingerprint that must remain stable before the first write.
type SuiteDeploymentAdapter interface {
	ID() ID
	SupportsReconcile() bool
	Prepare(context.Context, agentsuite.DeploymentMember, SuiteMemberBinding, SuiteDeployRequest) (SuiteMemberPlan, error)
	Inspect(context.Context, SuiteMemberPlan) (string, error)
	Deploy(context.Context, SuiteMemberPlan) (SuiteMemberOutcome, error)
}

type PreparedSuiteDeployment struct {
	request   SuiteDeployRequest
	adapter   SuiteDeploymentAdapter
	members   []SuiteMemberPlan
	inspected []string
	digest    string
}

type SuitePlanSummary struct {
	Instance       string               `json:"instance"`
	Suite          string               `json:"suite"`
	LogicalDigest  string               `json:"logicalDigest"`
	ArtifactDigest string               `json:"artifactDigest,omitempty"`
	PlanDigest     string               `json:"planDigest"`
	Target         SuiteTarget          `json:"target"`
	Members        []SuiteMemberSummary `json:"members"`
}
type SuiteMemberSummary struct {
	ConfigurationDigest string   `json:"configurationDigest,omitempty"`
	Agent               string   `json:"agent"`
	Name                string   `json:"name"`
	Digest              string   `json:"digest"`
	Resources           []string `json:"resources"`
}
type SuiteDeploymentReceipt struct {
	Plan    SuitePlanSummary     `json:"plan"`
	State   string               `json:"state"`
	Members []SuiteMemberOutcome `json:"members"`
}

func PrepareSuiteDeployment(ctx context.Context, request SuiteDeployRequest, adapters ...SuiteDeploymentAdapter) (*PreparedSuiteDeployment, error) {
	if request.Suite == nil || request.Instance == "" || request.Target.Context == "" || request.Target.ClusterUID == "" || request.Target.Namespace == "" {
		return nil, errors.New("suite, instance and explicit destination identity are required")
	}
	var selected SuiteDeploymentAdapter
	seen := map[ID]bool{}
	for _, adapter := range adapters {
		if adapter == nil || adapter.ID() == "" || seen[adapter.ID()] {
			return nil, errors.New("invalid or duplicate suite deployment adapter")
		}
		seen[adapter.ID()] = true
		if adapter.ID() == request.Target.Runtime {
			selected = adapter
		}
	}
	if selected == nil {
		return nil, &UnknownRuntimeError{Runtime: request.Target.Runtime}
	}
	if request.Reconcile && !selected.SupportsReconcile() {
		return nil, &UnsupportedVerbError{Runtime: selected.ID(), Verb: "suite reconciliation"}
	}
	members := request.Suite.Members()
	if len(members) == 0 || len(request.Bindings) != len(members) {
		return nil, errors.New("bindings must cover every suite member exactly once")
	}
	prepared := &PreparedSuiteDeployment{request: request, adapter: selected}
	resources := map[string]bool{}
	// Prepare every member before any cluster inspection or mutation.
	for _, member := range members {
		binding, ok := request.Bindings[member.Agent.ID]
		if !ok {
			return nil, fmt.Errorf("missing binding for agent %s", member.Agent.ID)
		}
		plan, err := selected.Prepare(ctx, member, binding, request)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", member.Agent.ID, err)
		}
		if plan.Agent != member.Agent.ID || plan.Name == "" || plan.Digest == "" || len(plan.Resources) == 0 {
			return nil, errors.New("adapter returned incomplete member plan")
		}
		for _, resource := range plan.Resources {
			if resources[resource] {
				return nil, fmt.Errorf("suite resource collision: %s", resource)
			}
			resources[resource] = true
		}
		prepared.members = append(prepared.members, plan)
	}
	data, err := json.Marshal(struct {
		Instance, Logical, Artifact string
		Target                      SuiteTarget
		Bindings                    map[string]SuiteMemberBinding
		Reconcile                   bool
		Members                     []SuiteMemberSummary
	}{request.Instance, request.Suite.LogicalDigest(), request.ArtifactDigest, request.Target, request.Bindings, request.Reconcile, prepared.Summary().Members})
	if err != nil {
		return nil, err
	}
	prepared.digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	return prepared, nil
}

func (p *PreparedSuiteDeployment) Summary() SuitePlanSummary {
	out := SuitePlanSummary{Instance: p.request.Instance, Suite: p.request.Suite.Name(), LogicalDigest: p.request.Suite.LogicalDigest(), ArtifactDigest: p.request.ArtifactDigest, PlanDigest: p.digest, Target: p.request.Target}
	for _, member := range p.members {
		out.Members = append(out.Members, SuiteMemberSummary{Agent: member.Agent, Name: member.Name, Digest: member.Digest, ConfigurationDigest: member.ConfigurationDigest, Resources: append([]string(nil), member.Resources...)})
	}
	return out
}

func (p *PreparedSuiteDeployment) Inspect(ctx context.Context) error {
	p.inspected = nil
	var fingerprints []string
	for _, member := range p.members {
		if err := ctx.Err(); err != nil {
			return err
		}
		fingerprint, err := p.adapter.Inspect(ctx, member)
		if err != nil {
			return fmt.Errorf("agent %s preflight: %w", member.Agent, err)
		}
		if strings.TrimSpace(fingerprint) == "" {
			return errors.New("adapter returned no inspection identity")
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	p.inspected = fingerprints
	return nil
}

// Deploy executes only a completely prepared and inspected suite. The gate and
// target confirmation callback is application-owned and cannot be omitted.
func (p *PreparedSuiteDeployment) Deploy(ctx context.Context, confirm func(SuitePlanSummary) error) (SuiteDeploymentReceipt, error) {
	receipt := SuiteDeploymentReceipt{Plan: p.Summary(), State: "not-deployed"}
	for _, member := range p.members {
		receipt.Members = append(receipt.Members, SuiteMemberOutcome{Agent: member.Agent, State: "not-attempted"})
	}
	if len(p.inspected) != len(p.members) || confirm == nil {
		return receipt, errors.New("suite requires complete inspection and confirmation before deployment")
	}
	if err := confirm(p.Summary()); err != nil {
		return receipt, err
	}
	for i, member := range p.members {
		fingerprint, err := p.adapter.Inspect(ctx, member)
		if err != nil {
			return receipt, err
		}
		if fingerprint != p.inspected[i] {
			return receipt, fmt.Errorf("agent %s destination changed after review; replan suite", member.Agent)
		}
	}
	for i, member := range p.members {
		if err := ctx.Err(); err != nil {
			return receipt, err
		}
		outcome, err := p.adapter.Deploy(ctx, member)
		outcome.Agent = member.Agent
		if err != nil {
			outcome.State = "unknown"
			receipt.Members[i] = outcome
			receipt.State = "partial"
			return receipt, fmt.Errorf("agent %s deployment stopped; inspect partial outcomes before retrying: %w", member.Agent, err)
		}
		if outcome.State != "ready" {
			receipt.State = "partial"
			outcome.State = "unknown"
			receipt.Members[i] = outcome
			return receipt, errors.New("adapter did not establish member readiness")
		}
		receipt.Members[i] = outcome
		receipt.State = "partial"
	}
	receipt.State = "ready"
	return receipt, nil
}
