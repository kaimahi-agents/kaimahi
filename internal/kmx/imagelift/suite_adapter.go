package imagelift

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// SuiteAdapter is the HTTP implementation of the shared suite deployment
// interface. It does not claim native platform registration or delegation.
type SuiteAdapter struct {
	Cluster Cluster
	Plans   map[string]Plan
}

func (SuiteAdapter) ID() agentruntime.ID     { return agentruntime.ID(agentsuite.ExecutionHTTPV1) }
func (SuiteAdapter) SupportsReconcile() bool { return true }
func (a SuiteAdapter) Prepare(_ context.Context, member agentsuite.DeploymentMember, binding agentruntime.SuiteMemberBinding, request agentruntime.SuiteDeployRequest) (agentruntime.SuiteMemberPlan, error) {
	plan, ok := a.Plans[member.Agent.ID]
	if !ok || plan.Name != binding.Name || plan.Image != binding.Image || plan.SuiteDigest != request.ArtifactDigest || plan.ClusterUID != request.Target.ClusterUID || plan.Namespace != request.Target.Namespace || plan.Context != request.Target.Context {
		return agentruntime.SuiteMemberPlan{}, errors.New("missing or mismatched resolved member image plan")
	}
	if len(member.Agent.Invokes) != 0 || len(member.Agent.ToolProviders) != 0 {
		return agentruntime.SuiteMemberPlan{}, errors.New("HTTP adapter cannot enforce delegation or install ToolProviders")
	}
	var resources []string
	configurationDigest := ""
	for _, object := range plan.Objects {
		resources = append(resources, plan.Namespace+"/"+object["kind"].(string)+"/"+plan.Name)
		if object["kind"] == "ConfigMap" {
			configurationDigest = fmt.Sprintf("%x", sha256.Sum256([]byte(object["data"].(Object)["agent.json"].(string))))
		}
	}
	return agentruntime.SuiteMemberPlan{Agent: member.Agent.ID, Name: plan.Name, Digest: plan.Digest, ConfigurationDigest: configurationDigest, Resources: resources, Payload: plan}, nil
}

func (a SuiteAdapter) Inspect(ctx context.Context, member agentruntime.SuiteMemberPlan) (string, error) {
	plan, ok := member.Payload.(Plan)
	if !ok {
		return "", errors.New("invalid HTTP member plan")
	}
	if err := Inspect(ctx, a.Cluster, plan); err != nil {
		return "", err
	}
	var identities []string
	for _, obj := range plan.Objects {
		current, err := readResource(ctx, a.Cluster, plan, obj)
		if err != nil {
			return "", err
		}
		if current == nil {
			identities = append(identities, "absent")
		} else {
			identities = append(identities, current.Metadata.UID+"/"+current.Metadata.ResourceVersion)
		}
	}
	data, _ := json.Marshal(identities)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data)), nil
}

func (a SuiteAdapter) Deploy(ctx context.Context, member agentruntime.SuiteMemberPlan) (agentruntime.SuiteMemberOutcome, error) {
	plan, ok := member.Payload.(Plan)
	if !ok {
		return agentruntime.SuiteMemberOutcome{}, errors.New("invalid HTTP member plan")
	}
	receipt, err := Deploy(ctx, a.Cluster, plan, func() error { return nil })
	out := agentruntime.SuiteMemberOutcome{Agent: member.Agent, State: "unknown"}
	if receipt.Ready {
		out.State = "ready"
	}
	for _, resource := range receipt.Resources {
		out.Resources = append(out.Resources, agentruntime.ResourceResult{Kind: resource.Kind, Name: resource.Name, Namespace: plan.Namespace, UID: resource.UID, Outcome: agentruntime.ResourceOutcome(resource.Outcome)})
	}
	return out, err
}
