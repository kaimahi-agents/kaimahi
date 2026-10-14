package workspace

import (
	"context"
	"errors"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

// PackagedCase holds the full authored bytes bound by native case metadata.
type PackagedCase struct {
	Path   string
	Digest string
	Bytes  []byte
}

// NativeSuite is one verified native member. Digest identifies the OCI manifest
// (including raw cases); GraphDigest identifies only the canonical public graph.
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
		return nil, errors.New("packaged native suite verification failed")
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
		return nil, errors.New("packaged native suite verification failed")
	}
	return loadNativeFiles(ctx, files, artifact.Descriptor().Digest.String())
}

// HasImageProfile only selects validation; it confers no execution authority.
func HasImageProfile(artifact *suiteoras.Artifact) bool {
	if artifact == nil {
		return false
	}
	_, exists := artifact.Files()[imagePath]
	return exists
}

func ValidateImageArtifact(ctx context.Context, artifact *suiteoras.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !HasImageProfile(artifact) {
		return errors.New("image execution profile required")
	}
	_, err := validateNative(artifact.Files())
	return err
}

// ImageSuite retains the closed image profile without substituting native model
// selection. Cases and public graph identities come from the exact artifact.
type ImageSuite struct {
	Digest, Name, MemberID, GraphDigest string
	Instructions                        []byte
	Cases                               []PackagedCase
}

func LoadImageArtifact(ctx context.Context, artifact *suiteoras.Artifact) (*ImageSuite, error) {
	if err := ValidateImageArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	files := artifact.Files()
	var suite spec.Suite
	var record nativeRecord
	if spec.Unmarshal(files["agentsuite.json"], &suite) != nil || spec.Unmarshal(files[imagePath], &record) != nil {
		return nil, errors.New("invalid image package")
	}
	g, err := spec.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, err
	}
	out := &ImageSuite{Digest: artifact.Descriptor().Digest.String(), Name: suite.Name, MemberID: record.Member, GraphDigest: g}
	var member spec.Agent
	if spec.Unmarshal(files[suite.Agents[0].Path], &member) != nil {
		return nil, errors.New("invalid image member")
	}
	out.Instructions = files[member.Instructions.Path]
	for _, c := range record.Cases {
		out.Cases = append(out.Cases, PackagedCase{Path: c.Path, Digest: c.Digest, Bytes: files[c.Path]})
	}
	return out, nil
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
	var member spec.Agent
	if err := spec.Unmarshal(files[suite.Agents[0].Path], &member); err != nil {
		return nil, errors.New("invalid packaged native member")
	}
	graphDigest, err := spec.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, errors.New("invalid packaged graph identity")
	}
	// Both entry points guarantee exactly one member, composition and profile,
	// with complete ordered cases and a caller-owned map of verified bytes.
	result := &NativeSuite{
		Digest:             digest,
		GraphDigest:        graphDigest,
		Name:               suite.Name,
		MemberID:           member.ID,
		Description:        member.Description,
		Model:              member.Model.Model,
		Instructions:       files[member.Instructions.Path],
		MemberDigest:       suite.Agents[0].Digest,
		InstructionsDigest: member.Instructions.Digest,
		CompositionDigest:  suite.Compositions[0].Digest,
		BuildProfileDigest: suite.BuildProfiles[0].Digest,
		RendererVersion:    native.RendererVersion,
		Runtime:            profile,
		ExecutionProfile:   native.ExecutionProfile,
		Cases:              make([]PackagedCase, 0, len(native.Cases)),
		RequiredImages:     []string{},
	}
	for _, c := range native.Cases {
		result.Cases = append(result.Cases, PackagedCase{Path: c.Path, Digest: c.Digest, Bytes: files[c.Path]})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
