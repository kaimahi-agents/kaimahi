package workspace

import (
	"bytes"
	"context"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

// PackagedCase carries exact case bytes bound by the suite's native metadata.
type PackagedCase struct {
	Path   string
	Digest string
	Bytes  []byte
}

// NativeSuite is the verified packaged input for native execution. Digest is
// the original OCI manifest identity; GraphDigest is the public root JCS identity.
// Declared build-profile images are not required native execution images.
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

// Load returns owned copies of the verified packaged member and complete case
// inventory. It does not read authored source or the preparation cache record.
func Load(ctx context.Context, p *Prepared) (*NativeSuite, error) {
	files, err := verify(ctx, p)
	if err != nil {
		return nil, err
	}
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return nil, err
	}
	var record nativeRecord
	if err := spec.Unmarshal(files[nativePath], &record); err != nil {
		return nil, err
	}
	var agent spec.Agent
	if err := spec.Unmarshal(files[suite.Agents[0].Path], &agent); err != nil {
		return nil, err
	}
	graphDigest, err := spec.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, err
	}
	// verify has established the closed one-member/one-composition/profile
	// graph and stable, full native case coverage before any result is built.
	result := &NativeSuite{
		Digest:             p.Descriptor.Digest.String(),
		GraphDigest:        graphDigest,
		Name:               suite.Name,
		MemberID:           agent.ID,
		Description:        agent.Description,
		Model:              agent.Model.Model,
		Instructions:       bytes.Clone(files[agent.Instructions.Path]),
		MemberDigest:       suite.Agents[0].Digest,
		InstructionsDigest: agent.Instructions.Digest,
		CompositionDigest:  suite.Compositions[0].Digest,
		BuildProfileDigest: suite.BuildProfiles[0].Digest,
		RendererVersion:    record.RendererVersion,
		Cases:              make([]PackagedCase, 0, len(record.Cases)),
		RequiredImages:     []string{},
	}
	for _, c := range record.Cases {
		result.Cases = append(result.Cases, PackagedCase{Path: c.Path, Digest: c.Digest, Bytes: bytes.Clone(files[c.Path])})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
