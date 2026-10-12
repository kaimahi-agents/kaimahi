package workspace

import (
	"context"
	"errors"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
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
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return nil, errors.New("invalid packaged suite manifest")
	}
	var native nativeRecord
	if err := spec.Unmarshal(files[nativePath], &native); err != nil {
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
	// verify guarantees exactly one member, composition and profile, with a
	// complete ordered case inventory. Its fresh map shares no durable cache data.
	result := &NativeSuite{
		Digest:             p.Descriptor.Digest.String(),
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
