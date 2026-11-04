package workspace

import (
	"bytes"
	"context"
	"errors"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	godigest "github.com/opencontainers/go-digest"
)

func resolveMapped(ctx context.Context, input *mappedInput) (*snapshot, error) {
	files, err := resolveMappedBase(ctx, input)
	if err != nil {
		return nil, err
	}
	i := input.value
	index := localartifact.SourceIndex{SchemaVersion: 1, PackageProfile: localartifact.SourceProfile, SourceFormat: i.Sources.Format, SourceSchemaVersion: i.Sources.SchemaVersion, PackagerVersion: localartifact.SourcePackager, MemberID: i.MemberID}
	for n, original := range [][]byte{i.Sources.Agent, i.Sources.Tests} {
		role, name := "agent", "metadata/kmx/source/"+i.MemberID+".agent.md"
		if n == 1 {
			role, name = "tests", "metadata/kmx/source/"+i.MemberID+".agent.test.md"
		}
		files[name] = bytes.Clone(original)
		index.Sources = append(index.Sources, localartifact.SourceRecord{Role: role, Path: name, Digest: godigest.FromBytes(original).String(), Size: int64(len(original))})
	}
	files[localartifact.SourceIndexPath], err = suitecore.Marshal(index)
	if err != nil {
		return nil, errMappedInput
	}
	if _, err := validateNative(files); err != nil {
		return nil, errMappedInput
	}
	fingerprint, err := mappedFingerprint(files)
	if err != nil {
		return nil, errMappedInput
	}
	return &snapshot{name: i.SuiteName, files: files, fingerprint: fingerprint}, nil
}

func mappedFingerprint(files map[string][]byte) (string, error) {
	hashes := make(map[string]string, len(files))
	for name, data := range files {
		hashes[name] = godigest.FromBytes(data).String()
	}
	frame, err := suitecore.Marshal(struct {
		Domain          string            `json:"domain"`
		PackagerVersion string            `json:"packagerVersion"`
		PackageProfile  string            `json:"packageProfile"`
		Files           map[string]string `json:"files"`
	}{"kmx.mapped-input/v1", localartifact.SourcePackager, localartifact.SourceProfile, hashes})
	if err != nil {
		return "", err
	}
	return suitecore.Digest(frame)
}

func validateSourceNative(files map[string][]byte) (string, error) {
	index, base, err := localartifact.SourceProfileBase(files)
	if err != nil {
		return "", err
	}
	if hasRuntimeProfiles(base) {
		return "", errMappedInput
	}
	if _, multi := base[multiNativePath]; multi {
		return "", errMappedInput
	}
	record, profile, err := nativeProfile(base)
	if err != nil || profile != (RuntimeProfile{ID: "orka"}) || record.Member != index.MemberID {
		return "", errMappedInput
	}
	var agent suitecore.Agent
	if suitecore.Unmarshal(base["agents/"+record.Member+".json"], &agent) != nil || agent.Policy != nil {
		return "", errMappedInput
	}
	_, cases, err := packagedCases(base, agent)
	if err != nil {
		return "", errMappedInput
	}
	for _, c := range cases {
		for _, a := range c.Assertions {
			if a.Type == "toolCalled" || a.Type == "toolNotCalled" {
				return "", errMappedInput
			}
		}
	}
	return validateNativeBase(base)
}

// ReadSources returns copied originals from a verified closed source profile.
// This grants no editable-source freshness, execution or retained-handle authority.
func ReadSources(ctx context.Context, artifact *suiteoras.Artifact) (SourceBundle, error) {
	if err := ctx.Err(); err != nil {
		return SourceBundle{}, err
	}
	if artifact == nil {
		return SourceBundle{}, errors.New("source package unavailable")
	}
	files := artifact.Files()
	if _, err := validateSourceNative(files); err != nil {
		return SourceBundle{}, verificationFailed(err)
	}
	index, _, err := localartifact.SourceProfileBase(files)
	if err != nil {
		return SourceBundle{}, verificationFailed(err)
	}
	return SourceBundle{Format: index.SourceFormat, SchemaVersion: index.SourceSchemaVersion, Agent: bytes.Clone(files[index.Sources[0].Path]), Tests: bytes.Clone(files[index.Sources[1].Path])}, nil
}
