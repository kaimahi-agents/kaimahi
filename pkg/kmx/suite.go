package kmx

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	MediaTypeOCIImageManifest = "application/vnd.oci.image.manifest.v1+json"
	ArtifactTypeAgentSuite    = "application/vnd.agentsuite.suite.v1"
)

var suiteIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func validateSuiteIdentifier(name, value string) error {
	if !suiteIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s must be an AgentSuite identifier", name)
	}
	return nil
}

func validateArtifactDigest(name, value string) error {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must be sha256 followed by 64 lowercase hexadecimal characters", name)
	}
	if _, err := ParseDigest(strings.TrimPrefix(value, prefix)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// OCIManifestDigest is the digest of an OCI image manifest descriptor.
type OCIManifestDigest string

func (d OCIManifestDigest) Validate() error {
	return validateArtifactDigest("OCI manifest digest", string(d))
}

func (d OCIManifestDigest) MarshalText() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return []byte(d), nil
}

func (d *OCIManifestDigest) UnmarshalText(text []byte) error {
	value := OCIManifestDigest(text)
	if err := value.Validate(); err != nil {
		return err
	}
	*d = value
	return nil
}

// CompositionDigest is the JCS digest of one exact AgentSuite composition.
type CompositionDigest string

func (d CompositionDigest) Validate() error {
	return validateArtifactDigest("composition digest", string(d))
}

func (d CompositionDigest) MarshalText() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return []byte(d), nil
}

func (d *CompositionDigest) UnmarshalText(text []byte) error {
	value := CompositionDigest(text)
	if err := value.Validate(); err != nil {
		return err
	}
	*d = value
	return nil
}

// SandboxBindingDigest is the JCS digest of the binding embedded in a derived
// Agent Sandbox Image.
type SandboxBindingDigest string

func (d SandboxBindingDigest) Validate() error {
	return validateArtifactDigest("sandbox binding digest", string(d))
}

func (d SandboxBindingDigest) MarshalText() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return []byte(d), nil
}

func (d *SandboxBindingDigest) UnmarshalText(text []byte) error {
	value := SandboxBindingDigest(text)
	if err := value.Validate(); err != nil {
		return err
	}
	*d = value
	return nil
}

// OCIArtifactRef identifies an OCI manifest. Location is a retrieval hint such
// as an OCI image-layout path or repository; Digest is the authoritative
// immutable identity, so Location never needs to repeat the digest.
type OCIArtifactRef struct {
	Location     string            `json:"location"`
	Digest       OCIManifestDigest `json:"digest"`
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType,omitempty"`
}

func (r OCIArtifactRef) Validate() error {
	if err := validateIdentity("OCI artifact location", r.Location); err != nil {
		return err
	}
	if strings.Contains(r.Location, "@sha256:") {
		return fmt.Errorf("OCI artifact location must not repeat the authoritative digest")
	}
	if err := r.Digest.Validate(); err != nil {
		return err
	}
	if err := validateIdentity("OCI artifact media type", r.MediaType); err != nil {
		return err
	}
	if r.ArtifactType != "" {
		return validateIdentity("OCI artifact type", r.ArtifactType)
	}
	return nil
}

type ociArtifactRefJSON OCIArtifactRef

func (r OCIArtifactRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(ociArtifactRefJSON(r))
}

func (r *OCIArtifactRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded ociArtifactRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := OCIArtifactRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// SuiteSource points to an extracted AgentSuite content directory or OCI image
// layout. It is an in-process input, not a wire format.
type SuiteSource struct {
	Path string
}

func (s SuiteSource) Validate() error {
	return validateIdentity("AgentSuite source path", s.Path)
}

func (SuiteSource) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("SuiteSource is an in-process value; persist its OCI artifact identity")
}

func (*SuiteSource) UnmarshalJSON([]byte) error {
	return fmt.Errorf("SuiteSource is an in-process value; reopen its source path")
}

type SandboxPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

func (p SandboxPlatform) Validate() error {
	if p.OS != "linux" {
		return fmt.Errorf("sandbox platform must use linux")
	}
	if p.Architecture != "amd64" && p.Architecture != "arm64" {
		return fmt.Errorf("sandbox architecture must be amd64 or arm64")
	}
	return nil
}

type SuiteAgentPlatform struct {
	Agent     string            `json:"agent"`
	Platforms []SandboxPlatform `json:"platforms"`
}

type SuiteReport struct {
	Name                  string                      `json:"name"`
	Agents                int                         `json:"agents"`
	Tools                 int                         `json:"tools"`
	ToolCompositions      int                         `json:"toolCompositions"`
	Compositions          int                         `json:"compositions"`
	Capabilities          []string                    `json:"capabilities,omitempty"`
	AgentPlatforms        []SuiteAgentPlatform        `json:"agentPlatforms,omitempty"`
	CompositionSelections []SuiteCompositionSelection `json:"compositionSelections,omitempty"`
}

type SuiteCompositionSelection struct {
	Agent        string            `json:"agent"`
	Platform     SandboxPlatform   `json:"platform"`
	BuildProfile string            `json:"buildProfile"`
	Digest       CompositionDigest `json:"digest"`
}

func (r SuiteReport) Validate() error {
	if err := validateSuiteIdentifier("AgentSuite name", r.Name); err != nil {
		return err
	}
	if r.Agents <= 0 || r.Compositions <= 0 || len(r.CompositionSelections) != r.Compositions {
		return fmt.Errorf("AgentSuite report requires one exact selection per composition")
	}
	seen := make(map[string]bool, len(r.CompositionSelections))
	for _, selection := range r.CompositionSelections {
		if err := validateSuiteIdentifier("composition agent", selection.Agent); err != nil {
			return err
		}
		if err := selection.Platform.Validate(); err != nil {
			return err
		}
		if err := validateSuiteIdentifier("composition build profile", selection.BuildProfile); err != nil {
			return err
		}
		if err := selection.Digest.Validate(); err != nil {
			return err
		}
		key := selection.Agent + "@" + selection.Platform.OS + "/" + selection.Platform.Architecture
		if seen[key] {
			return fmt.Errorf("duplicate AgentSuite composition selection %q", key)
		}
		seen[key] = true
	}
	return nil
}

func (r SuiteReport) Composition(agent string, platform SandboxPlatform) (SuiteCompositionSelection, error) {
	if err := r.Validate(); err != nil {
		return SuiteCompositionSelection{}, err
	}
	for _, selection := range r.CompositionSelections {
		if selection.Agent == agent && selection.Platform == platform {
			return selection, nil
		}
	}
	return SuiteCompositionSelection{}, fmt.Errorf("AgentSuite has no composition for %s on %s/%s", agent, platform.OS, platform.Architecture)
}

// AgentSuiteArtifact is an immutable OCI-distributed definition. It is not
// directly runnable.
type AgentSuiteArtifact struct {
	Manifest OCIArtifactRef `json:"manifest"`
}

func (a AgentSuiteArtifact) Validate() error {
	if err := a.Manifest.Validate(); err != nil {
		return err
	}
	if a.Manifest.MediaType != MediaTypeOCIImageManifest || a.Manifest.ArtifactType != ArtifactTypeAgentSuite {
		return fmt.Errorf("AgentSuite artifact requires OCI image manifest media type and AgentSuite artifact type")
	}
	return nil
}

type agentSuiteArtifactJSON AgentSuiteArtifact

func (a AgentSuiteArtifact) MarshalJSON() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(agentSuiteArtifactJSON(a))
}

func (a *AgentSuiteArtifact) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded agentSuiteArtifactJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := AgentSuiteArtifact(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*a = value
	return nil
}

type PackageSuiteResult struct {
	Artifact AgentSuiteArtifact `json:"artifact"`
	Report   SuiteReport        `json:"report"`
}

func (r PackageSuiteResult) Validate() error {
	if err := r.Artifact.Validate(); err != nil {
		return err
	}
	return r.Report.Validate()
}

type packageSuiteResultJSON PackageSuiteResult

func (r PackageSuiteResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(packageSuiteResultJSON(r))
}

func (r *PackageSuiteResult) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded packageSuiteResultJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := PackageSuiteResult(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

type ValidateSuiteRequest struct {
	Source SuiteSource
}

type PackageSuiteRequest struct {
	Operation OperationID
	Source    SuiteSource
	Output    string
}

func (r PackageSuiteRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	if err := r.Source.Validate(); err != nil {
		return err
	}
	return validateIdentity("AgentSuite OCI layout output", r.Output)
}

// AgentSandboxImage is a runnable OCI image derived for exactly one AgentSuite,
// agent, platform, build profile, composition, and embedded binding.
type AgentSandboxImage struct {
	Image         OCIArtifactRef       `json:"image"`
	SuiteManifest OCIManifestDigest    `json:"suiteManifest"`
	Agent         string               `json:"agent"`
	Platform      SandboxPlatform      `json:"platform"`
	BuildProfile  string               `json:"buildProfile"`
	Composition   CompositionDigest    `json:"composition"`
	Binding       SandboxBindingDigest `json:"binding"`
}

// AgentSandboxRef is the immutable identity persisted in deployments. Retrieval
// location is intentionally excluded because publication may change it without
// changing image identity.
type AgentSandboxRef struct {
	ImageManifest OCIManifestDigest    `json:"imageManifest"`
	MediaType     string               `json:"mediaType"`
	SuiteManifest OCIManifestDigest    `json:"suiteManifest"`
	Agent         string               `json:"agent"`
	Platform      SandboxPlatform      `json:"platform"`
	BuildProfile  string               `json:"buildProfile"`
	Composition   CompositionDigest    `json:"composition"`
	Binding       SandboxBindingDigest `json:"binding"`
}

func (r AgentSandboxRef) Validate() error {
	if err := r.ImageManifest.Validate(); err != nil {
		return err
	}
	if r.MediaType != MediaTypeOCIImageManifest {
		return fmt.Errorf("agent sandbox must use OCI image manifest media type")
	}
	if err := r.SuiteManifest.Validate(); err != nil {
		return err
	}
	if err := validateSuiteIdentifier("agent ID", r.Agent); err != nil {
		return err
	}
	if err := r.Platform.Validate(); err != nil {
		return err
	}
	if err := validateSuiteIdentifier("build profile", r.BuildProfile); err != nil {
		return err
	}
	if err := r.Composition.Validate(); err != nil {
		return err
	}
	return r.Binding.Validate()
}

func (i AgentSandboxImage) Ref() AgentSandboxRef {
	return AgentSandboxRef{
		ImageManifest: i.Image.Digest,
		MediaType:     i.Image.MediaType,
		SuiteManifest: i.SuiteManifest,
		Agent:         i.Agent, Platform: i.Platform, BuildProfile: i.BuildProfile,
		Composition: i.Composition, Binding: i.Binding,
	}
}

func (i AgentSandboxImage) Validate() error {
	if err := i.Image.Validate(); err != nil {
		return err
	}
	if i.Image.MediaType != MediaTypeOCIImageManifest || i.Image.ArtifactType != "" {
		return fmt.Errorf("agent sandbox image must identify a runnable OCI image manifest")
	}
	return i.Ref().Validate()
}

type agentSandboxImageJSON AgentSandboxImage

func (i AgentSandboxImage) MarshalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(agentSandboxImageJSON(i))
}

func (i *AgentSandboxImage) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded agentSandboxImageJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := AgentSandboxImage(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*i = value
	return nil
}

type BuildSandboxRequest struct {
	Operation    OperationID
	Suite        AgentSuiteArtifact
	Agent        string
	Platform     SandboxPlatform
	BuildProfile string
	Composition  CompositionDigest
	Output       string
}

func (r BuildSandboxRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	if err := r.Suite.Validate(); err != nil {
		return err
	}
	if err := validateSuiteIdentifier("agent ID", r.Agent); err != nil {
		return err
	}
	if err := r.Platform.Validate(); err != nil {
		return err
	}
	if err := validateSuiteIdentifier("build profile", r.BuildProfile); err != nil {
		return err
	}
	if err := r.Composition.Validate(); err != nil {
		return err
	}
	return validateIdentity("sandbox OCI layout output", r.Output)
}

type PublishArtifactRequest struct {
	Operation   OperationID
	Artifact    OCIArtifactRef
	Destination string
}

func (r PublishArtifactRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	if err := r.Artifact.Validate(); err != nil {
		return err
	}
	return validateIdentity("OCI registry destination", r.Destination)
}

// AgentSuites owns definition validation, offline OCI construction, and
// explicit publication. It does not deploy sandbox images to an environment.
type AgentSuites interface {
	Validate(context.Context, ValidateSuiteRequest) (SuiteReport, error)
	Package(context.Context, PackageSuiteRequest) (PackageSuiteResult, error)
	RecoverPackage(context.Context, OperationID) (PackageSuiteResult, error)
	BuildSandbox(context.Context, BuildSandboxRequest) (AgentSandboxImage, error)
	RecoverBuildSandbox(context.Context, OperationID) (AgentSandboxImage, error)
	Publish(context.Context, PublishArtifactRequest) (OCIArtifactRef, error)
	RecoverPublish(context.Context, OperationID) (OCIArtifactRef, error)
}
