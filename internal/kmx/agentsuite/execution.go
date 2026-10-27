package agentsuite

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
)

const (
	ImageDeploymentLabel     = "org.agentsuite.image-deployment"
	ImageDeploymentMediaType = "application/vnd.agentsuite.image.deployment.v1+json"
	AgentKitConfigPath       = "/run/agentsuite/agent.json"
)

// ImageDeployment is a digest-bound publisher declaration, not a conformant
// sandbox binding, filesystem inventory or signed build provenance.
type ImageDeployment struct {
	Inference     *suitecore.InferenceRequirements `json:"inference,omitempty"`
	SchemaVersion string                           `json:"schemaVersion"`
	MediaType     string                           `json:"mediaType"`
	// SuiteReference is an optional legacy retrieval hint, never source identity.
	// New builds omit it so copying a suite repository does not change the image.
	SuiteReference     string                      `json:"suiteReference,omitempty"`
	SuiteDigest        string                      `json:"suiteDigest"`
	Agent              string                      `json:"agent"`
	Platform           suitecore.Platform          `json:"platform"`
	CompositionDigest  string                      `json:"compositionDigest"`
	BuildProfile       string                      `json:"buildProfile"`
	BuildProfileDigest string                      `json:"buildProfileDigest"`
	Execution          suitecore.ExecutionContract `json:"execution"`
}

func DecodeImageDeployment(data []byte) (*ImageDeployment, error) {
	if len(data) > 16<<10 {
		return nil, errors.New("image declaration exceeds 16 KiB")
	}
	var r ImageDeployment
	var fields map[string]json.RawMessage
	if err := suitecore.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range []string{"schemaVersion", "mediaType", "suiteDigest", "agent", "platform", "compositionDigest", "buildProfile", "buildProfileDigest", "execution"} {
		if raw, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("image declaration requires explicit non-null identity and execution fields")
		}
	}
	for _, name := range []string{"inference", "suiteReference"} {
		if raw, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("optional image declaration fields must not be null")
		}
	}
	if err := suitecore.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if _, present := fields["suiteReference"]; present && r.SuiteReference == "" {
		return nil, errors.New("suiteReference must not be empty when present")
	}
	if r.SchemaVersion != suitecore.SpecVersion || r.MediaType != ImageDeploymentMediaType || !suitecore.IsValidDigest(r.SuiteDigest) || !suitecore.IsValidDigest(r.CompositionDigest) || !suitecore.IsValidDigest(r.BuildProfileDigest) || !builderIdentifierPattern.MatchString(r.Agent) || !builderIdentifierPattern.MatchString(r.BuildProfile) {
		return nil, errors.New("invalid image deployment identity")
	}
	if r.SuiteReference != "" {
		if !strings.HasSuffix(r.SuiteReference, "@"+r.SuiteDigest) {
			return nil, errors.New("suiteReference must pin suiteDigest")
		}
		if err := suitecore.ValidateImageReference(r.SuiteReference); err != nil {
			return nil, err
		}
	}
	if err := validateBindingPlatform(r.Platform); err != nil {
		return nil, err
	}
	if err := suitecore.ValidateExecutionContract(r.Execution); err != nil {
		return nil, err
	}
	if (r.Inference != nil) != (r.Execution.Configuration == suitecore.AgentKitMountedConfig) {
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
	data, err := suitecore.Marshal(r)
	if err != nil {
		return "", err
	}
	if _, err := DecodeImageDeployment(data); err != nil {
		return "", err
	}
	return string(data), nil
}
