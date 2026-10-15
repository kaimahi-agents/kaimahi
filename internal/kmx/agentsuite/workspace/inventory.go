package workspace

import (
	"context"
	"errors"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

// NativeInventory is an offline snapshot, not execution authority or evaluation
// evidence. Every member carries the same aggregate suite and artifact identity.
// All returned bytes are owned by the caller.
type NativeInventory struct {
	Digest      string
	GraphDigest string
	Name        string
	Members     []NativeSuite
}

// LoadInventory verifies and reads every member of a prepared snapshot.
func LoadInventory(ctx context.Context, p *Prepared) (*NativeInventory, error) {
	files, err := verify(ctx, p)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, verificationFailed(err)
	}
	return loadInventoryFiles(ctx, files, p.Descriptor.Digest.String())
}

// LoadArtifactInventory reads the full closed inventory without workspace source
// or preparation records. The supplied artifact has already verified OCI digests.
func LoadArtifactInventory(ctx context.Context, artifact *suiteoras.Artifact) (*NativeInventory, error) {
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
	return loadInventoryFiles(ctx, files, artifact.Descriptor().Digest.String())
}

func loadInventoryFiles(ctx context.Context, files map[string][]byte, digest string) (*NativeInventory, error) {
	if _, multi := files[multiNativePath]; !multi {
		member, err := loadNativeFiles(ctx, files, digest)
		if err != nil {
			return nil, err
		}
		return &NativeInventory{Digest: digest, GraphDigest: member.GraphDigest, Name: member.Name, Members: []NativeSuite{*member}}, nil
	}
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return nil, errors.New("invalid packaged suite manifest")
	}
	var metadata multiRecord
	if err := spec.Unmarshal(files[multiNativePath], &metadata); err != nil {
		return nil, errors.New("invalid packaged native metadata")
	}
	graphDigest, err := spec.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, errors.New("invalid packaged graph identity")
	}
	result := &NativeInventory{Digest: digest, GraphDigest: graphDigest, Name: suite.Name, Members: make([]NativeSuite, 0, len(metadata.Members))}
	for i, record := range metadata.Members {
		profile := RuntimeProfile{ID: "orka"}
		if record.Runtime != nil {
			profile = *record.Runtime
		}
		member, err := loadNativeMember(ctx, files, suite, record, profile, digest, graphDigest, i)
		if err != nil {
			return nil, err
		}
		result.Members = append(result.Members, *member)
	}
	return result, ctx.Err()
}
