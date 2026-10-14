package kmx

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	MediaTypeOCIImageManifest = "application/vnd.oci.image.manifest.v1+json"
	ArtifactTypeAgentSuite    = "application/vnd.agentsuite.suite.v1"
)

var (
	suiteIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	suiteCapabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)
)

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
	if r.Agents <= 0 || r.Agents > r.Compositions || r.Tools < 0 || r.ToolCompositions < 0 ||
		r.Compositions <= 0 || len(r.CompositionSelections) != r.Compositions {
		return fmt.Errorf("AgentSuite report requires one exact selection per composition")
	}
	for i, capability := range r.Capabilities {
		if !suiteCapabilityPattern.MatchString(capability) || i > 0 && r.Capabilities[i-1] >= capability {
			return fmt.Errorf("AgentSuite report capabilities must be valid, sorted and unique")
		}
	}
	seen := make(map[string]bool, len(r.CompositionSelections))
	agents := make(map[string]map[SandboxPlatform]bool, len(r.CompositionSelections))
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
		if agents[selection.Agent] == nil {
			agents[selection.Agent] = make(map[SandboxPlatform]bool)
		}
		agents[selection.Agent][selection.Platform] = true
	}
	if len(agents) != r.Agents {
		return fmt.Errorf("AgentSuite report agent count does not match composition selections")
	}
	if len(r.AgentPlatforms) != 0 {
		if len(r.AgentPlatforms) != r.Agents {
			return fmt.Errorf("AgentSuite report requires one platform record per agent")
		}
		listed := make(map[string]bool, len(r.AgentPlatforms))
		for _, item := range r.AgentPlatforms {
			if err := validateSuiteIdentifier("agent platform ID", item.Agent); err != nil {
				return err
			}
			expected, ok := agents[item.Agent]
			if !ok || listed[item.Agent] || len(item.Platforms) != len(expected) {
				return fmt.Errorf("AgentSuite report agent platforms do not match composition selections")
			}
			listed[item.Agent] = true
			platforms := make(map[SandboxPlatform]bool, len(item.Platforms))
			for _, platform := range item.Platforms {
				if err := platform.Validate(); err != nil {
					return err
				}
				if platforms[platform] || !expected[platform] {
					return fmt.Errorf("AgentSuite report agent platforms do not match composition selections")
				}
				platforms[platform] = true
			}
		}
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

// AgentSuiteArtifactRef is the location-free identity of one AgentSuite
// artifact. Retrieval locations belong to in-process operation values and may
// change without changing this reference.
type AgentSuiteArtifactRef struct {
	Manifest OCIManifestDigest `json:"manifest"`
}

func (r AgentSuiteArtifactRef) Validate() error {
	return r.Manifest.Validate()
}

type agentSuiteArtifactRefJSON AgentSuiteArtifactRef

func (r AgentSuiteArtifactRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(agentSuiteArtifactRefJSON(r))
}

func (r *AgentSuiteArtifactRef) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded agentSuiteArtifactRefJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := AgentSuiteArtifactRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (a AgentSuiteArtifact) Ref() AgentSuiteArtifactRef {
	return AgentSuiteArtifactRef{Manifest: a.Manifest.Digest}
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

// ValidatedAgentSuite is the exact immutable AgentSuite artifact materialized
// from a validated source and the report derived from that same graph. Build
// consumes this value rather than reopening the mutable SuiteSource path. This
// value is operation evidence, not authentication: implementations revalidate
// the artifact before using the report as authoritative build input.
type ValidatedAgentSuite struct {
	Artifact AgentSuiteArtifact `json:"artifact"`
	Report   SuiteReport        `json:"report"`
}

func (s ValidatedAgentSuite) Ref() AgentSuiteArtifactRef {
	return s.Artifact.Ref()
}

func (s ValidatedAgentSuite) Validate() error {
	if err := s.Artifact.Validate(); err != nil {
		return err
	}
	return s.Report.Validate()
}

type validatedAgentSuiteJSON ValidatedAgentSuite

func (s ValidatedAgentSuite) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(validatedAgentSuiteJSON(s))
}

func (s *ValidatedAgentSuite) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded validatedAgentSuiteJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := ValidatedAgentSuite(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*s = value
	return nil
}

type ValidateSuiteRequest struct {
	Source SuiteSource
}

func (r ValidateSuiteRequest) Validate() error {
	return r.Source.Validate()
}

// BuildSuiteRequest asks KMX to build every agent/platform composition in one
// exact validated suite through the initial image-backed execution profile.
// Builder selection, temporary output locations and member-level planning
// remain implementation details. Image-free native profiles are valid
// AgentSuites but are outside this first Build contract.
type BuildSuiteRequest struct {
	Operation OperationID
	Suite     ValidatedAgentSuite
}

func (r BuildSuiteRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	return r.Suite.Validate()
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

// BuildSuiteResult is the complete member-image set for the initial image-backed
// execution profile. It deliberately has no independent release identity: a
// future general build contract must derive required artifacts, including zero
// images, from an explicit execution profile.
type BuildSuiteResult struct {
	Operation OperationID         `json:"operation"`
	Suite     ValidatedAgentSuite `json:"suite"`
	Images    []AgentSandboxImage `json:"images"`
}

func (r BuildSuiteResult) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	return validateSuiteImages(r.Suite, r.Images, true)
}

func (r BuildSuiteResult) ValidateAgainst(request BuildSuiteRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if r.Operation != request.Operation || r.Suite.Ref() != request.Suite.Ref() ||
		!sameSuiteReport(r.Suite.Report, request.Suite.Report) {
		return fmt.Errorf("AgentSuite build result does not match the request")
	}
	return nil
}

type buildSuiteResultJSON BuildSuiteResult

func (r BuildSuiteResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(buildSuiteResultJSON(r))
}

func (r *BuildSuiteResult) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded buildSuiteResultJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := BuildSuiteResult(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// BuildSuiteProgress is durable recovery state for a suite build. Images may
// be a verified subset while Complete is false. Complete progress must satisfy
// the same whole-suite checks as BuildSuiteResult.
type BuildSuiteProgress struct {
	Operation OperationID         `json:"operation"`
	Suite     ValidatedAgentSuite `json:"suite"`
	Images    []AgentSandboxImage `json:"images"`
	Complete  bool                `json:"complete"`
}

func (p BuildSuiteProgress) Validate() error {
	if err := p.Operation.Validate(); err != nil {
		return err
	}
	return validateSuiteImages(p.Suite, p.Images, p.Complete)
}

func (p BuildSuiteProgress) ValidateAgainst(request BuildSuiteRequest) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if p.Operation != request.Operation || p.Suite.Ref() != request.Suite.Ref() ||
		!sameSuiteReport(p.Suite.Report, request.Suite.Report) {
		return fmt.Errorf("AgentSuite build progress does not match the request")
	}
	return nil
}

func (p BuildSuiteProgress) Result() (BuildSuiteResult, error) {
	if err := p.Validate(); err != nil {
		return BuildSuiteResult{}, err
	}
	if !p.Complete {
		return BuildSuiteResult{}, fmt.Errorf("AgentSuite build is not complete")
	}
	return BuildSuiteResult{Operation: p.Operation, Suite: p.Suite, Images: append([]AgentSandboxImage(nil), p.Images...)}, nil
}

type buildSuiteProgressJSON BuildSuiteProgress

func (p BuildSuiteProgress) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(buildSuiteProgressJSON(p))
}

func (p *BuildSuiteProgress) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded buildSuiteProgressJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := BuildSuiteProgress(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*p = value
	return nil
}

// PushSuiteRequest publishes one already-built suite result. Push may relocate
// content but must not rebuild it or change any immutable identity.
type PushSuiteRequest struct {
	Operation   OperationID      `json:"operation"`
	Build       BuildSuiteResult `json:"build"`
	Destination string           `json:"destination"`
}

func (r PushSuiteRequest) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	if err := r.Build.Validate(); err != nil {
		return err
	}
	return validateIdentity("AgentSuite push destination", r.Destination)
}

// PushSuiteResult contains the same complete suite and member-image identities
// with destination retrieval locations. ValidateAgainst establishes that a
// publisher preserved the build input rather than rebuilding or substituting it.
type PushSuiteResult struct {
	Operation   OperationID         `json:"operation"`
	Destination string              `json:"destination"`
	Suite       ValidatedAgentSuite `json:"suite"`
	Images      []AgentSandboxImage `json:"images"`
}

func (r PushSuiteResult) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	if err := validateIdentity("AgentSuite push destination", r.Destination); err != nil {
		return err
	}
	if r.Suite.Artifact.Manifest.Location != r.Destination {
		return fmt.Errorf("pushed AgentSuite artifact is not located at the recorded destination")
	}
	for _, image := range r.Images {
		if image.Image.Location != r.Destination {
			return fmt.Errorf("pushed member image %s is not located at the recorded destination", image.Agent)
		}
	}
	return validateSuiteImages(r.Suite, r.Images, true)
}

func (r PushSuiteResult) ValidateAgainst(request PushSuiteRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if r.Operation != request.Operation || r.Destination != request.Destination {
		return fmt.Errorf("pushed AgentSuite result does not match the requested operation and destination")
	}
	build := request.Build
	if r.Suite.Ref() != build.Suite.Ref() || !sameSuiteReport(r.Suite.Report, build.Suite.Report) {
		return fmt.Errorf("pushed AgentSuite does not match the built suite")
	}
	want := make(map[AgentSandboxRef]bool, len(build.Images))
	for _, image := range build.Images {
		want[image.Ref()] = true
	}
	for _, image := range r.Images {
		if !want[image.Ref()] {
			return fmt.Errorf("pushed AgentSuite contains a substituted member image")
		}
		delete(want, image.Ref())
	}
	if len(want) != 0 {
		return fmt.Errorf("pushed AgentSuite omits a built member image")
	}
	return nil
}

type pushSuiteResultJSON PushSuiteResult

func (r PushSuiteResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(pushSuiteResultJSON(r))
}

func (r *PushSuiteResult) UnmarshalJSON(data []byte) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	var decoded pushSuiteResultJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := PushSuiteResult(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func validateSuiteImages(suite ValidatedAgentSuite, images []AgentSandboxImage, complete bool) error {
	if err := suite.Validate(); err != nil {
		return err
	}
	if complete && suite.Report.ToolCompositions != 0 {
		return fmt.Errorf("complete AgentSuite builds cannot yet represent standalone ToolProvider images")
	}
	if complete && len(images) == 0 {
		return fmt.Errorf("the image-backed AgentSuite workflow requires member images")
	}
	expected := make(map[string]SuiteCompositionSelection, len(suite.Report.CompositionSelections))
	for _, selection := range suite.Report.CompositionSelections {
		expected[suiteImageSelectionKey(selection.Agent, selection.Platform)] = selection
	}
	seen := make(map[string]bool, len(images))
	for _, image := range images {
		if err := image.Validate(); err != nil {
			return err
		}
		if image.SuiteManifest != suite.Artifact.Manifest.Digest {
			return fmt.Errorf("member image %s is bound to another AgentSuite artifact", image.Agent)
		}
		key := suiteImageSelectionKey(image.Agent, image.Platform)
		selection, ok := expected[key]
		if !ok {
			return fmt.Errorf("member image %s has no matching AgentSuite composition", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate member image %s", key)
		}
		seen[key] = true
		if image.BuildProfile != selection.BuildProfile || image.Composition != selection.Digest {
			return fmt.Errorf("member image %s does not match its AgentSuite composition", key)
		}
	}
	if complete && len(seen) != len(expected) {
		return fmt.Errorf("complete AgentSuite build requires one image for every composition")
	}
	return nil
}

func suiteImageSelectionKey(agent string, platform SandboxPlatform) string {
	return agent + "@" + platform.OS + "/" + platform.Architecture
}

func sameSuiteReport(a, b SuiteReport) bool {
	if a.Name != b.Name || a.Agents != b.Agents || a.Tools != b.Tools ||
		a.ToolCompositions != b.ToolCompositions || a.Compositions != b.Compositions ||
		!slices.Equal(a.Capabilities, b.Capabilities) ||
		!slices.Equal(a.CompositionSelections, b.CompositionSelections) ||
		len(a.AgentPlatforms) != len(b.AgentPlatforms) {
		return false
	}
	for i := range a.AgentPlatforms {
		if a.AgentPlatforms[i].Agent != b.AgentPlatforms[i].Agent ||
			!slices.Equal(a.AgentPlatforms[i].Platforms, b.AgentPlatforms[i].Platforms) {
			return false
		}
	}
	return true
}

// AgentSuites owns the cohesive validate, image-backed build and push workflow.
// It does not expose member builders, storage transports, runtime adapters or
// deployment. Image-free execution profiles remain valid but are outside this
// initial Build contract.
type AgentSuites interface {
	Validate(context.Context, ValidateSuiteRequest) (ValidatedAgentSuite, error)
	Build(context.Context, BuildSuiteRequest) (BuildSuiteResult, error)
	RecoverBuild(context.Context, OperationID) (BuildSuiteProgress, error)
	Push(context.Context, PushSuiteRequest) (PushSuiteResult, error)
	RecoverPush(context.Context, OperationID) (PushSuiteResult, error)
}
