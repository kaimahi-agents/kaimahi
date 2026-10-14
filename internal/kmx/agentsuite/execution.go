package agentsuite

import (
	"errors"
	"reflect"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

const (
	ImageDeploymentLabel     = "org.agentsuite.image-deployment"
	ImageDeploymentMediaType = "application/vnd.agentsuite.image.deployment.v1+json"
	AgentKitConfigPath       = "/run/agentsuite/agent.json"
)

// ImageDeployment is a digest-bound publisher declaration, not a conformant
// sandbox binding, filesystem inventory or signed build provenance.
type ImageDeployment struct {
	Inference     *spec.InferenceRequirements `json:"inference,omitempty"`
	SchemaVersion string                      `json:"schemaVersion"`
	MediaType     string                      `json:"mediaType"`
	// SuiteReference is an optional legacy retrieval hint, never source identity.
	// New builds omit it so copying a suite repository does not change the image.
	SuiteReference     string                 `json:"suiteReference,omitempty"`
	SuiteDigest        string                 `json:"suiteDigest"`
	Agent              string                 `json:"agent"`
	Platform           spec.Platform          `json:"platform"`
	CompositionDigest  string                 `json:"compositionDigest"`
	BuildProfile       string                 `json:"buildProfile"`
	BuildProfileDigest string                 `json:"buildProfileDigest"`
	Execution          spec.ExecutionContract `json:"execution"`
}

func DecodeImageDeployment(data []byte) (*ImageDeployment, error) {
	if len(data) > 16<<10 {
		return nil, errors.New("image declaration exceeds 16 KiB")
	}
	var r ImageDeployment
	if err := spec.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.SchemaVersion != spec.SpecVersion || r.MediaType != ImageDeploymentMediaType || !spec.IsValidDigest(r.SuiteDigest) || !spec.IsValidDigest(r.CompositionDigest) || !spec.IsValidDigest(r.BuildProfileDigest) || !builderIdentifierPattern.MatchString(r.Agent) || !builderIdentifierPattern.MatchString(r.BuildProfile) {
		return nil, errors.New("invalid image deployment identity")
	}
	if r.SuiteReference != "" {
		if !strings.HasSuffix(r.SuiteReference, "@"+r.SuiteDigest) {
			return nil, errors.New("suiteReference must pin suiteDigest")
		}
		if err := spec.ValidateImageReference(r.SuiteReference); err != nil {
			return nil, err
		}
	}
	if err := validateBindingPlatform(r.Platform); err != nil {
		return nil, err
	}
	if err := spec.ValidateExecutionContract(r.Execution); err != nil {
		return nil, err
	}
	if (r.Inference != nil) != (r.Execution.Configuration == spec.AgentKitMountedConfig) {
		return nil, errors.New("mounted configuration requires inference capabilities")
	}
	if r.Inference != nil {
		if err := r.Inference.Validate(); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func EncodeImageDeployment(r ImageDeployment) (string, error) {
	data, err := spec.Marshal(r)
	if err != nil {
		return "", err
	}
	if _, err := DecodeImageDeployment(data); err != nil {
		return "", err
	}
	return string(data), nil
}

// ValidateImageSource reuses the current member build resolver so lift and build
// compare the same public graph and exact instruction/composition/profile bytes.
func ValidateImageSource(root string, r ImageDeployment) error {
	if _, err := EncodeImageDeployment(r); err != nil {
		return err
	}
	plan, err := ResolveSandboxPlan(root, BuildSelection{Agent: r.Agent, Platform: r.Platform.String()})
	if err != nil {
		return err
	}
	if plan.CompositionDigest != r.CompositionDigest || plan.BuildProfile.ID != r.BuildProfile || plan.BuildProfileDigest != r.BuildProfileDigest {
		return errors.New("image composition/profile differs from suite")
	}
	if !reflect.DeepEqual(plan.Agent.Model.Capabilities, r.Inference) || plan.BuildProfile.Execution == nil || !reflect.DeepEqual(*plan.BuildProfile.Execution, r.Execution) {
		return errors.New("image execution/inference differs from suite")
	}
	if len(plan.Agent.Invokes) != 0 || len(plan.ToolProviders) != 0 {
		return errors.New("HTTP execution does not support ToolProviders or invocation edges")
	}
	if plan.Agent.Model.Protocol != "openai-compatible" || len(plan.Agent.Model.SecretRefs) != 0 {
		return errors.New("unsupported HTTP model requirements")
	}
	if plan.Agent.Model.EndpointEnv != "" {
		found := false
		for _, slot := range r.Execution.Inputs {
			found = found || slot.Environment == plan.Agent.Model.EndpointEnv && !slot.Secret
		}
		if !found {
			return errors.New("model endpoint has no execution input")
		}
	}
	return nil
}
