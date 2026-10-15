package workspace

import (
	"context"
	"errors"
	"fmt"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

// PackagedCase holds public JSON bytes bound by the member's evaluation index.
type PackagedCase struct {
	Path   string
	Digest string
	Bytes  []byte
}

// NativeSuite is one verified native member. Digest identifies the OCI manifest
// (including public cases); GraphDigest identifies the canonical public graph.
// Build-profile images are declared build inputs, not native execution images.
type NativeSuite struct {
	Digest             string
	GraphDigest        string
	Name               string
	MemberID           string
	Description        string
	Model              string
	Instructions       []byte
	MemberDigest       string
	InstructionsDigest string
	CompositionDigest  string
	BuildProfileDigest string
	RendererVersion    string
	Runtime            RuntimeProfile
	ExecutionProfile   string
	Cases              []PackagedCase
	RequiredImages     []string
}

// Load reads only the single verified packaged snapshot, not live workspace
// source or prepared.json. The returned bytes are owned by the caller.
func Load(ctx context.Context, p *Prepared) (*NativeSuite, error) {
	files, err := verify(ctx, p)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Strict verifier diagnostics can quote untrusted metadata keys or values.
		return nil, verificationFailed(err)
	}
	if _, multi := files[multiNativePath]; multi {
		return nil, ErrMultiMemberUnsupported
	}
	return loadNativeFiles(ctx, files, p.Descriptor.Digest.String())
}

// LoadArtifact validates the closed native profile from a digest-verified
// snapshot. Source fingerprints, names and preparation records are not inputs.
func LoadArtifact(ctx context.Context, artifact *suiteoras.Artifact) (*NativeSuite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, errors.New("packaged native suite verification failed")
	}
	files := artifact.Files()
	if _, err := validateNative(files); err != nil {
		return nil, verificationFailed(err)
	}
	if _, multi := files[multiNativePath]; multi {
		return nil, ErrMultiMemberUnsupported
	}
	return loadNativeFiles(ctx, files, artifact.Descriptor().Digest.String())
}

// verificationFailed exposes only refusal reasons.
func verificationFailed(err error) error {
	var r refusal
	if errors.As(err, &r) {
		return fmt.Errorf("packaged native suite verification failed: %w", r)
	}
	return errors.New("packaged native suite verification failed")
}

func loadNativeFiles(ctx context.Context, files map[string][]byte, digest string) (*NativeSuite, error) {
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return nil, errors.New("invalid packaged suite manifest")
	}
	native, profile, err := nativeProfile(files)
	if err != nil {
		return nil, errors.New("invalid packaged native metadata")
	}
	graphDigest, err := spec.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, errors.New("invalid packaged graph identity")
	}
	return loadNativeMember(ctx, files, suite, native, profile, digest, graphDigest, 0)
}

func loadNativeMember(ctx context.Context, files map[string][]byte, suite spec.Suite, native nativeRecord, profile RuntimeProfile, digest, graphDigest string, index int) (*NativeSuite, error) {
	var member spec.Agent
	if err := spec.Unmarshal(files[suite.Agents[index].Path], &member); err != nil {
		return nil, errors.New("invalid packaged native member")
	}
	cases, _, err := packagedCases(files, member)
	if err != nil {
		return nil, errors.New("invalid packaged native evaluation cases")
	}
	// All entry points guarantee complete ordered cases and a caller-owned map
	// of verified bytes; inventory loaders pass the selected member's index.
	result := &NativeSuite{
		Digest:             digest,
		GraphDigest:        graphDigest,
		Name:               suite.Name,
		MemberID:           member.ID,
		Description:        member.Description,
		Model:              member.Model.Model,
		Instructions:       files[member.Instructions.Path],
		MemberDigest:       suite.Agents[index].Descriptor.Digest,
		InstructionsDigest: member.Instructions.Descriptor.Digest,
		CompositionDigest:  suite.Compositions[index].Descriptor.Digest,
		BuildProfileDigest: suite.BuildProfiles[0].Descriptor.Digest,
		RendererVersion:    native.RendererVersion,
		Runtime:            profile,
		ExecutionProfile:   native.ExecutionProfile,
		Cases:              cases,
		RequiredImages:     []string{},
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
