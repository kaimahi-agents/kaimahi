package agentsuite

import (
	"errors"
	"regexp"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

const MediaTypeSandboxBinding = "application/vnd.agentsuite.sandbox.binding.v1+json"

var builderIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// SandboxBinding records the immutable inputs and final inventory selected by
// the kmx sandbox-image builder. It is implementation metadata, not part of
// the portable AgentSuite specification.
type SandboxBinding struct {
	SchemaVersion string          `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	SuiteDigest   string          `json:"suiteDigest"`
	Agent         string          `json:"agent"`
	Platform      spec.Platform   `json:"platform"`
	BuildProfile  string          `json:"buildProfile"`
	Composition   spec.Descriptor `json:"composition"`
	Inventory     spec.Descriptor `json:"inventory"`
}

// ValidateSandboxBinding validates the fixed binding record embedded by the
// kmx sandbox-image builder. Image filesystem and runtime probes are separate
// conformance operations.
func ValidateSandboxBinding(data []byte) (*SandboxBinding, error) {
	var binding SandboxBinding
	if err := spec.Unmarshal(data, &binding); err != nil {
		return nil, err
	}
	if binding.SchemaVersion != spec.SpecVersion || binding.MediaType != MediaTypeSandboxBinding {
		return nil, errors.New("sandbox binding has unsupported schemaVersion or mediaType")
	}
	if !spec.IsValidDigest(binding.SuiteDigest) || !builderIdentifierPattern.MatchString(binding.Agent) ||
		binding.BuildProfile == "" || !validBindingDescriptor(binding.Composition) ||
		binding.Composition.MediaType != spec.MediaTypeComposition || !validBindingDescriptor(binding.Inventory) {
		return nil, errors.New("sandbox binding identity or descriptors are invalid")
	}
	if err := validateBindingPlatform(binding.Platform); err != nil {
		return nil, err
	}
	return &binding, nil
}

func validBindingDescriptor(descriptor spec.Descriptor) bool {
	return descriptor.MediaType != "" && spec.IsValidDigest(descriptor.Digest) && descriptor.Size >= 0
}

func validateBindingPlatform(platform spec.Platform) error {
	if platform.OS != "linux" ||
		platform.Architecture != "amd64" && platform.Architecture != "arm64" ||
		platform.Variant != "" {
		return errors.New("platform must be exactly linux/amd64 or linux/arm64")
	}
	return nil
}
