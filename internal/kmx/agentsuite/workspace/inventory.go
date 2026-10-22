package workspace

import (
	"context"
	"errors"

	suitecore "github.com/agent-primitives/suite/core"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
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
	return loadInventoryFiles(ctx, files, recipeArtifactIdentity(p.Descriptor))
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
	return loadInventoryFiles(ctx, files, recipeArtifactIdentity(artifact.Descriptor()))
}

func loadInventoryFiles(ctx context.Context, files map[string][]byte, artifact suiteview.ArtifactIdentity) (*NativeInventory, error) {
	if hasRuntimeProfiles(files) {
		return nil, ErrProfileSelectionRequired
	}
	if _, multi := files[multiNativePath]; !multi {
		member, err := loadNativeFiles(ctx, files, artifact)
		if err != nil {
			return nil, err
		}
		return &NativeInventory{Digest: artifact.Digest, GraphDigest: member.GraphDigest, Name: member.Name, Members: []NativeSuite{*member}}, nil
	}
	var suite suitecore.Suite
	if err := suitecore.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return nil, errors.New("invalid packaged suite manifest")
	}
	var metadata multiRecord
	if err := suitecore.Unmarshal(files[multiNativePath], &metadata); err != nil {
		return nil, errors.New("invalid packaged native metadata")
	}
	graphDigest, err := suitecore.Digest(files["agentsuite.json"])
	if err != nil {
		return nil, errors.New("invalid packaged graph identity")
	}
	result := &NativeInventory{Digest: artifact.Digest, GraphDigest: graphDigest, Name: suite.Name, Members: make([]NativeSuite, 0, len(metadata.Members))}
	for i, record := range metadata.Members {
		profile := RuntimeProfile{ID: "orka"}
		if record.Runtime != nil {
			profile = *record.Runtime
		}
		member, err := loadNativeMember(ctx, files, suite, record, profile, artifact, graphDigest, i)
		if err != nil {
			return nil, err
		}
		result.Members = append(result.Members, *member)
	}
	return result, ctx.Err()
}
