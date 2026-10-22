package suiteview

import (
	"bytes"
	"context"
	"errors"
	"io"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
)

const (
	projectionFileBytes = 1 << 20
	projectionMembers   = 16
)

// Selection identifies one exact member. ManifestDigest may be omitted only
// when the OCI index has one unambiguous root.
type Selection struct {
	ManifestDigest string
	MemberID       string
}

// ArtifactIdentity is the exact media-type, digest, and size identity of the
// raw OCI manifest.
type ArtifactIdentity struct {
	MediaType string
	Digest    string
	Size      int64
}

// SuiteIdentity distinguishes the logical graph from its enclosing OCI artifact.
type SuiteIdentity struct {
	Name        string
	GraphDigest string
}

// MemberIdentity identifies the selected canonical Agent document.
type MemberIdentity struct {
	ID         string
	Descriptor suitecore.Descriptor
}

// MemberRecipe is a caller-owned read-only view of the member's portable
// behavioral core. It is not a complete execution plan or evidence of runtime
// support, policy enforcement, readiness, or evaluation.
type MemberRecipe struct {
	Artifact               ArtifactIdentity
	Suite                  SuiteIdentity
	Member                 MemberIdentity
	Description            string
	Instructions           []byte
	InstructionsDescriptor suitecore.Descriptor
	Model                  suitecore.ModelRequirement
}

// RefusalCode is a stable, payload-free refusal category.
type RefusalCode string

const (
	RefusalSelection         RefusalCode = "selection"
	RefusalArtifact          RefusalCode = "artifact"
	RefusalLimit             RefusalCode = "limit"
	RefusalAmbiguousArtifact RefusalCode = "ambiguous-artifact"
	RefusalArtifactNotFound  RefusalCode = "artifact-not-found"
	RefusalMemberNotFound    RefusalCode = "member-not-found"
	RefusalMember            RefusalCode = "member"
	RefusalCanceled          RefusalCode = "canceled"
)

// Refusal contains no untrusted paths, document values, or parser diagnostics.
type Refusal struct {
	Code  RefusalCode
	cause error
}

func (r *Refusal) Error() string {
	if r == nil {
		return "suite member view refused"
	}
	return "suite member view refused: " + string(r.Code)
}

func (r *Refusal) Unwrap() error {
	if r == nil {
		return nil
	}
	return r.cause
}

// LoadLayout loads from a local OCI image-layout directory without mutating it.
func LoadLayout(ctx context.Context, layout string, selection Selection) (MemberRecipe, error) {
	if err := validateSelection(ctx, selection); err != nil {
		return MemberRecipe{}, err
	}
	artifact, err := localartifact.ReadLayout(ctx, layout, selection.ManifestDigest)
	if err != nil {
		return MemberRecipe{}, refusalFor(ctx, err)
	}
	return project(ctx, artifact, selection.MemberID)
}

// LoadArchive loads an uncompressed OCI image-layout tar from an already-open
// ReaderAt. At most 64 MiB is frozen before validation and projection.
func LoadArchive(ctx context.Context, source io.ReaderAt, size int64, selection Selection) (MemberRecipe, error) {
	if err := validateSelection(ctx, selection); err != nil {
		return MemberRecipe{}, err
	}
	artifact, err := localartifact.ReadArchive(ctx, source, size, selection.ManifestDigest)
	if err != nil {
		return MemberRecipe{}, refusalFor(ctx, err)
	}
	return project(ctx, artifact, selection.MemberID)
}

// ProjectMember projects one member from an already verified, immutable
// artifact file snapshot. It rechecks the selected member and instruction
// descriptors, but does not validate or return an enclosing OCI identity.
// Callers that do not already hold such a snapshot should use LoadLayout or
// LoadArchive.
func ProjectMember(ctx context.Context, files map[string][]byte, memberID string) (recipe MemberRecipe, err error) {
	defer func() {
		if err != nil {
			recipe = MemberRecipe{}
		}
	}()
	if err := ctx.Err(); err != nil {
		return MemberRecipe{}, &Refusal{Code: RefusalCanceled, cause: err}
	}
	if memberID == "" {
		return MemberRecipe{}, &Refusal{Code: RefusalSelection}
	}
	rootBytes, ok := files["agentsuite.json"]
	if !ok {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	if len(rootBytes) > projectionFileBytes {
		return MemberRecipe{}, &Refusal{Code: RefusalLimit}
	}
	var suite suitecore.Suite
	if suitecore.Unmarshal(rootBytes, &suite) != nil {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	if len(suite.Agents) > projectionMembers {
		return MemberRecipe{}, &Refusal{Code: RefusalLimit}
	}
	graphDigest, err := suitecore.Digest(rootBytes)
	if err != nil {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	var refs []suitecore.ManifestRef
	for _, ref := range suite.Agents {
		if ref.ID == memberID {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return MemberRecipe{}, &Refusal{Code: RefusalMemberNotFound}
	}
	if len(refs) != 1 {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	ref := refs[0]
	memberBytes, ok := files[ref.Path]
	if len(memberBytes) > projectionFileBytes {
		return MemberRecipe{}, &Refusal{Code: RefusalLimit}
	}
	if !ok || ref.Descriptor.MediaType != suitecore.MediaTypeAgent || !suitecore.MatchesCanonicalDescriptor(memberBytes, ref.Descriptor) {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	var member suitecore.Agent
	if suitecore.Unmarshal(memberBytes, &member) != nil || member.ID != ref.ID || member.Instructions.Descriptor.MediaType != suitecore.MediaTypeInstructions {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	instructions, ok := files[member.Instructions.Path]
	if len(instructions) > projectionFileBytes {
		return MemberRecipe{}, &Refusal{Code: RefusalLimit}
	}
	if !ok || !suitecore.DescriptorSizeAndDigestMatch(instructions, member.Instructions.Descriptor) {
		return MemberRecipe{}, &Refusal{Code: RefusalMember}
	}
	recipe = MemberRecipe{
		Suite:                  SuiteIdentity{Name: suite.Name, GraphDigest: graphDigest},
		Member:                 MemberIdentity{ID: member.ID, Descriptor: cloneDescriptor(ref.Descriptor)},
		Description:            member.Description,
		Instructions:           bytes.Clone(instructions),
		InstructionsDescriptor: cloneDescriptor(member.Instructions.Descriptor),
		Model:                  cloneModel(member.Model),
	}
	if err := ctx.Err(); err != nil {
		return MemberRecipe{}, &Refusal{Code: RefusalCanceled, cause: err}
	}
	return recipe, nil
}

func validateSelection(ctx context.Context, selection Selection) error {
	if err := ctx.Err(); err != nil {
		return &Refusal{Code: RefusalCanceled, cause: err}
	}
	if selection.MemberID == "" || selection.ManifestDigest != "" && !suitecore.IsValidDigest(selection.ManifestDigest) {
		return &Refusal{Code: RefusalSelection}
	}
	return nil
}

func project(ctx context.Context, artifact *localartifact.Artifact, memberID string) (MemberRecipe, error) {
	if artifact == nil {
		return MemberRecipe{}, &Refusal{Code: RefusalArtifact}
	}
	descriptor := artifact.Descriptor()
	recipe, err := ProjectMember(ctx, artifact.Files(), memberID)
	if err != nil {
		return MemberRecipe{}, err
	}
	recipe.Artifact = ArtifactIdentity{
		MediaType: descriptor.MediaType,
		Digest:    descriptor.Digest.String(),
		Size:      descriptor.Size,
	}
	return recipe, nil
}

func refusalFor(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return &Refusal{Code: RefusalCanceled, cause: cause}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return &Refusal{Code: RefusalCanceled, cause: cause}
		}
	}
	switch {
	case errors.Is(err, localartifact.ErrArtifactLimit):
		return &Refusal{Code: RefusalLimit}
	case errors.Is(err, localartifact.ErrArtifactAmbiguous):
		return &Refusal{Code: RefusalAmbiguousArtifact}
	case errors.Is(err, localartifact.ErrArtifactNotFound):
		return &Refusal{Code: RefusalArtifactNotFound}
	default:
		return &Refusal{Code: RefusalArtifact}
	}
}

func cloneDescriptor(value suitecore.Descriptor) suitecore.Descriptor {
	if value.Annotations != nil {
		annotations := make(map[string]string, len(value.Annotations))
		for key, item := range value.Annotations {
			annotations[key] = item
		}
		value.Annotations = annotations
	}
	return value
}

func cloneModel(value suitecore.ModelRequirement) suitecore.ModelRequirement {
	if value.Capabilities != nil {
		capabilities := *value.Capabilities
		value.Capabilities = &capabilities
	}
	value.SecretRefs = append([]string(nil), value.SecretRefs...)
	return value
}
