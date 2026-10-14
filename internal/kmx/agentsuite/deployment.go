package agentsuite

import (
	"encoding/json"
	"os"
	"sort"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

// Internal deployment projections use the public format's types and validator.
// These aliases keep the carried prototype's adapter boundary source-compatible
// while its orchestration converges with the shared lifecycle contracts.
type Platform = spec.Platform
type Suite = spec.Suite
type Agent = spec.Agent
type ModelRequirement = spec.ModelRequirement
type InferenceRequirements = spec.InferenceRequirements
type ExecutionContract = spec.ExecutionContract
type ExecutionInput = spec.ExecutionInput
type BuildProfile = spec.BuildProfile
type FileRef = spec.FileRef
type ManifestRef = spec.ManifestRef
type Composition = spec.Composition
type CompositionRef = spec.CompositionRef
type ResolvedToolProvider = spec.ResolvedToolProvider
type ToolProviderCatalog = spec.ToolProviderCatalog
type ToolProvider = spec.ToolProvider
type ToolProviderComposition = spec.ToolProviderComposition
type SecretKeyRef = spec.SecretKeyRef
type Descriptor = spec.Descriptor

const (
	SpecVersion                  = spec.SpecVersion
	MediaTypeSuite               = spec.MediaTypeSuite
	MediaTypeAgent               = spec.MediaTypeAgent
	MediaTypeComposition         = spec.MediaTypeComposition
	MediaTypeBuildProfile        = spec.MediaTypeBuildProfile
	MediaTypeToolProviderCatalog = spec.MediaTypeToolProviderCatalog
	ExecutionHTTPV1              = spec.ExecutionHTTPV1
	AgentKitMountedConfig        = spec.AgentKitMountedConfig
)

type DeploymentMember struct {
	Agent             spec.Agent        `json:"agent"`
	Instructions      string            `json:"instructions"`
	Composition       spec.Composition  `json:"composition"`
	CompositionDigest string            `json:"compositionDigest"`
	BuildProfile      spec.BuildProfile `json:"buildProfile"`
}

type DeploymentSuite struct {
	suite     spec.Suite
	digest    string
	members   []DeploymentMember
	providers []spec.ToolProvider
}

func (s *DeploymentSuite) Name() string          { return s.suite.Name }
func (s *DeploymentSuite) LogicalDigest() string { return s.digest }
func (s *DeploymentSuite) Members() []DeploymentMember {
	data, _ := json.Marshal(s.members)
	var out []DeploymentMember
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *DeploymentSuite) ToolProviders() []spec.ToolProvider {
	data, _ := json.Marshal(s.providers)
	var out []spec.ToolProvider
	_ = json.Unmarshal(data, &out)
	return out
}

// ResolveDeploymentSuite reuses the current public validation and member build
// resolver. Every requested platform member must resolve before planning.
func ResolveDeploymentSuite(root string, platform spec.Platform) (*DeploymentSuite, error) {
	if err := validateBindingPlatform(platform); err != nil {
		return nil, err
	}
	if _, err := spec.ValidateDirectory(root); err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	raw, err := readBuildFile(handle, "agentsuite.json", maxBuildJSONBytes)
	if err != nil {
		return nil, err
	}
	v, err := loadBuildMetadata(handle, raw)
	if err != nil {
		return nil, err
	}
	digest, err := spec.Digest(raw)
	if err != nil {
		return nil, err
	}
	out := &DeploymentSuite{suite: v.suite, digest: digest}
	for _, ref := range v.suite.Agents {
		plan, err := ResolveSandboxPlan(root, BuildSelection{Agent: ref.ID, Platform: platform.String()})
		if err != nil {
			return nil, err
		}
		if plan.SuiteRootDigest != digest {
			return nil, os.ErrInvalid
		}
		out.members = append(out.members, DeploymentMember{plan.Agent, string(plan.Instructions), plan.Composition, plan.CompositionDigest, plan.BuildProfile})
	}
	for _, provider := range v.toolProviders {
		out.providers = append(out.providers, provider)
	}
	sort.Slice(out.members, func(i, j int) bool { return out.members[i].Agent.ID < out.members[j].Agent.ID })
	return out, nil
}
