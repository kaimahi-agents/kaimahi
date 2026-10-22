package workspace

import (
	"bytes"
	"context"
	"errors"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const maxSessionsAssociationBytes = 16 << 10

var errSessionsAssociation = errors.New("Sessions association refused (image declared-unverified; not execution evidence)")

// SessionsAssociationV1 is detached KMX consumer metadata, never suite content,
// image verification, supported-implementation qualification or execution proof.
// ImageVerification must remain declared-unverified even when declarations agree.
type SessionsAssociationV1 struct {
	Version            int                            `json:"version"`
	Kind               string                         `json:"kind"`
	SuiteDigest        string                         `json:"suiteDigest"`
	MemberID           string                         `json:"memberID"`
	RuntimeProfile     SessionsAssociationProfile     `json:"runtimeProfile"`
	CompositionDigest  string                         `json:"compositionDigest"`
	BuildProfileDigest string                         `json:"buildProfileDigest"`
	Platform           suiteevidence.EvidencePlatform `json:"platform"`
	HarnessImage       string                         `json:"harnessImage"`
	ExecutionKind      string                         `json:"executionKind"`
	ConfigDigest       string                         `json:"configDigest"`
	ImageVerification  string                         `json:"imageVerification"`
}

type SessionsAssociationProfile struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// NewSessionsAssociation returns canonical declaration bytes without modifying S
// or reading source, state, images or a registry. harnessImage must echo the
// selected build profile's exact reference; no mirror or free-form image authority
// is granted. Versions, renderer/ABI support and image bytes remain unqualified.
func NewSessionsAssociation(ctx context.Context, selected *NativeSuite, harnessImage string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if selected == nil || selected.profiled == nil || suitecore.ValidateImageReference(harnessImage) != nil {
		return nil, errSessionsAssociation
	}
	// Public fields are mutable views. Only this privately retained selection
	// and immutable same-S graph may supply identities and instruction bytes.
	graph := selected.profiled
	s, err := loadSelectedFiles(ctx, graph.files, graph.artifact, graph.selection)
	if err != nil || s.Profile == nil || s.Profile.Runtime.ID != "agentsessions" || s.Policy != nil {
		return nil, errSessionsAssociation
	}
	var root suitecore.Suite
	if suitecore.Unmarshal(graph.files["agentsuite.json"], &root) != nil {
		return nil, errSessionsAssociation
	}
	var build suitecore.BuildProfile
	found := false
	for _, ref := range root.BuildProfiles {
		if ref.Descriptor.Digest == s.BuildProfileDigest {
			if found || suitecore.Unmarshal(graph.files[ref.Path], &build) != nil {
				return nil, errSessionsAssociation
			}
			found = true
		}
	}
	if !found || build.Execution != nil {
		return nil, errSessionsAssociation
	}
	_, imageDigest, _ := strings.Cut(harnessImage, "@")
	matches := 0
	for _, image := range build.Harness {
		if image.Platform != s.Platform {
			continue
		}
		matches++
		if image.ImageRef != harnessImage || image.Image.Digest != imageDigest || image.Image.Digest != s.Profile.Harness.Digest || image.Image.MediaType != ocispec.MediaTypeImageManifest {
			return nil, errSessionsAssociation
		}
	}
	if matches != 1 {
		return nil, errSessionsAssociation
	}
	config, err := agentsessions.ConfigDigest(string(s.Instructions))
	if err != nil {
		return nil, errSessionsAssociation
	}
	raw, err := suitecore.Marshal(SessionsAssociationV1{
		Version: 1, Kind: "kmx.sessions-association", SuiteDigest: s.Digest, MemberID: s.MemberID,
		RuntimeProfile:    SessionsAssociationProfile{ID: s.Profile.ID, Digest: s.ProfileDigest},
		CompositionDigest: s.CompositionDigest, BuildProfileDigest: s.BuildProfileDigest,
		Platform:     suiteevidence.EvidencePlatform{OS: s.Platform.OS, Architecture: s.Platform.Architecture, Variant: s.Platform.Variant},
		HarnessImage: harnessImage, ExecutionKind: "harnesswire-chat-v1", ConfigDigest: config,
		ImageVerification: "declared-unverified",
	})
	if err != nil || len(raw) > maxSessionsAssociationBytes {
		return nil, errSessionsAssociation
	}
	return raw, ctx.Err()
}

// CheckSessionsAssociation checks only canonical declaration consistency. A nil
// error never qualifies an image/profile, authorizes execution or enables Apply.
func CheckSessionsAssociation(ctx context.Context, raw []byte, selected *NativeSuite) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxSessionsAssociationBytes {
		return errSessionsAssociation
	}
	var record SessionsAssociationV1
	if suitecore.Unmarshal(raw, &record) != nil {
		return errSessionsAssociation
	}
	expected, err := NewSessionsAssociation(ctx, selected, record.HarnessImage)
	if err != nil {
		return err
	}
	// Every field is mandatory, including empty platform.variant. Exact equality
	// to independently derived canonical bytes rejects omissions, nulls, changed
	// declarations and attempted verified/qualified claims, not just bad values.
	if !bytes.Equal(raw, expected) {
		return errSessionsAssociation
	}
	return ctx.Err()
}
