package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
)

const profiledLayoutVersion = "kmx.profiled-suite/v1"

var (
	ErrProfileSelectionRequired    = errors.New("profiled suite requires explicit --runtime-profile selection")
	ErrProfileExecutionUnsupported = errors.New("profiled suites support offline selection and explicit unevaluated transfer only; execution and private proof export/evaluated publication are unsupported")
	profileID                      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// Selection identifies an offline view; it never removes content from S.
type Selection struct{ Member, Platform, RuntimeProfile string }

type resolvedProfile struct {
	runtime  agentsuite.RuntimeProfile
	build    agentsuite.BuildProfile
	platform agentsuite.Platform
}

func parseProfilePlatform(value string) (agentsuite.Platform, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !profileID.MatchString(parts[0]) || !profileID.MatchString(parts[1]) {
		return agentsuite.Platform{}, errors.New("profile platform requires OS/ARCH")
	}
	return agentsuite.Platform{OS: parts[0], Architecture: parts[1]}, nil
}

func readSourceProfiles(rows []sourceProfile, read func(string) ([]byte, error)) ([]resolvedProfile, error) {
	result := make([]resolvedProfile, 0, len(rows))
	for _, row := range rows {
		raw, err := read(row.RuntimeProfile)
		if err != nil {
			return nil, fmt.Errorf("runtime profile source: %w", err)
		}
		var rp agentsuite.RuntimeProfile
		if agentsuite.Unmarshal(raw, &rp) != nil || !profileID.MatchString(rp.ID) || safeText(string(raw)) != nil {
			return nil, errors.New("invalid runtime profile JSON")
		}
		// Scan decoded values too: JSON escapes can conceal credential shapes.
		semantic, err := agentsuite.Marshal(rp)
		if err != nil || safeText(string(semantic)) != nil {
			return nil, errors.New("invalid runtime profile JSON")
		}
		raw, err = read(row.BuildProfile)
		if err != nil {
			return nil, fmt.Errorf("build profile source: %w", err)
		}
		var bp agentsuite.BuildProfile
		if agentsuite.Unmarshal(raw, &bp) != nil || !profileID.MatchString(bp.ID) || safeText(string(raw)) != nil {
			return nil, errors.New("invalid build profile JSON")
		}
		semantic, err = agentsuite.Marshal(bp)
		if err != nil || safeText(string(semantic)) != nil {
			return nil, errors.New("invalid build profile JSON")
		}
		platform, err := parseProfilePlatform(row.Platform)
		if err != nil {
			return nil, err
		}
		result = append(result, resolvedProfile{runtime: rp, build: bp, platform: platform})
	}
	return result, nil
}

func resolveProfiled(name string, m normalizedMember, profiles []resolvedProfile) (map[string][]byte, error) {
	if !scaffold.ValidSuiteName(name) || name == "." || name == ".." || len(profiles) == 0 || len(profiles) > 16 || m.member.Runtime != nil {
		return nil, errors.New("unsupported profiled workspace")
	}
	if err := validateMember(m.member, m.instructions); err != nil {
		return nil, err
	}
	files, agent, catalog, err := resolveMemberGraph(m.member, m.instructions, m.cases)
	if err != nil {
		return nil, err
	}
	suite := agentsuite.Suite{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.MediaTypeSuite, Name: name, Agents: []agentsuite.ManifestRef{agent}, ToolProviderCatalog: catalog, BuildProfiles: []agentsuite.ManifestRef{}, RuntimeProfiles: []agentsuite.ManifestRef{}, Compositions: []agentsuite.CompositionRef{}}
	put := func(p, media, id string, value any) (agentsuite.ManifestRef, error) {
		raw, err := agentsuite.Marshal(value)
		if err != nil {
			return agentsuite.ManifestRef{}, err
		}
		if prior, ok := files[p]; ok && !bytes.Equal(prior, raw) {
			return agentsuite.ManifestRef{}, errors.New("conflicting same-ID profile documents")
		}
		files[p] = raw
		digest, err := agentsuite.Digest(raw)
		return agentsuite.ManifestRef{ID: id, Path: p, Descriptor: agentsuite.Descriptor{MediaType: media, Digest: digest, Size: int64(len(raw))}}, err
	}
	profiles = slices.Clone(profiles)
	slices.SortFunc(profiles, func(a, b resolvedProfile) int {
		return strings.Compare(a.runtime.ID+"/"+a.platform.String(), b.runtime.ID+"/"+b.platform.String())
	})
	runtimes, builds, tuples := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range profiles {
		rp, err := put("runtime-profiles/"+p.runtime.ID+".json", agentsuite.MediaTypeRuntimeProfile, p.runtime.ID, p.runtime)
		if err != nil {
			return nil, err
		}
		bp, err := put("build-profiles/"+p.build.ID+".json", agentsuite.MediaTypeBuildProfile, p.build.ID, p.build)
		if err != nil {
			return nil, err
		}
		if !runtimes[rp.ID] {
			suite.RuntimeProfiles = append(suite.RuntimeProfiles, rp)
			runtimes[rp.ID] = true
		}
		if !builds[bp.ID] {
			suite.BuildProfiles = append(suite.BuildProfiles, bp)
			builds[bp.ID] = true
		}
		key := rp.ID + "/" + p.platform.String()
		if tuples[key] {
			return nil, errors.New("duplicate profile/platform tuple")
		}
		tuples[key] = true
		composition := agentsuite.Composition{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.MediaTypeComposition, Agent: m.member.ID, Platform: p.platform, RuntimeProfile: rp.ID, BuildProfile: bp.ID, ToolProviders: []agentsuite.ResolvedToolProvider{}}
		ref, err := put("compositions/"+m.member.ID+"/"+key+".json", agentsuite.MediaTypeComposition, "", composition)
		if err != nil {
			return nil, err
		}
		suite.Compositions = append(suite.Compositions, agentsuite.CompositionRef{Agent: m.member.ID, Platform: p.platform, RuntimeProfile: rp.ID, Path: ref.Path, Descriptor: ref.Descriptor})
	}
	slices.SortFunc(suite.BuildProfiles, func(a, b agentsuite.ManifestRef) int { return strings.Compare(a.ID, b.ID) })
	raw, err := agentsuite.Marshal(suite)
	if err != nil {
		return nil, err
	}
	files["agentsuite.json"] = raw
	return files, nativeBounds(files)
}

func hasRuntimeProfiles(files map[string][]byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(files["agentsuite.json"], &root) != nil {
		return false
	}
	_, present := root["runtimeProfiles"]
	return present
}

// This path is closed independently; legacy regeneration remains exact.
func validateProfiled(files map[string][]byte) (string, error) {
	var suite agentsuite.Suite
	if agentsuite.Unmarshal(files["agentsuite.json"], &suite) != nil || len(suite.Agents) != 1 || len(suite.Compositions) == 0 || len(suite.Compositions) > 16 {
		return "", errors.New("unsupported profiled graph")
	}
	var agent agentsuite.Agent
	if agentsuite.Unmarshal(files[suite.Agents[0].Path], &agent) != nil {
		return "", errors.New("invalid profiled member")
	}
	m := sourceMember{ID: agent.ID, Description: agent.Description}
	m.Model.Protocol, m.Model.Model = agent.Model.Protocol, agent.Model.Model
	_, cases, err := packagedCases(files, agent)
	if err != nil {
		return "", refusal{err}
	}
	// Policy is a shared declaration, not an Orka/Sessions enforcement claim.
	m.policyBytes, err = readDeclaredMemberPolicy(files, agent)
	if err != nil {
		return "", err
	}
	var rows []sourceProfile
	for _, ref := range suite.Compositions {
		var comp agentsuite.Composition
		if agentsuite.Unmarshal(files[ref.Path], &comp) != nil {
			return "", errors.New("invalid profiled composition")
		}
		row := sourceProfile{Platform: ref.Platform.String()}
		for _, p := range suite.RuntimeProfiles {
			if p.ID == ref.RuntimeProfile {
				row.RuntimeProfile = p.Path
			}
		}
		for _, p := range suite.BuildProfiles {
			if p.ID == comp.BuildProfile {
				row.BuildProfile = p.Path
			}
		}
		rows = append(rows, row)
	}
	profiles, err := readSourceProfiles(rows, func(name string) ([]byte, error) {
		raw, ok := files[name]
		if !ok {
			return nil, errors.New("missing profile document")
		}
		return raw, nil
	})
	if err != nil {
		return "", err
	}
	expected, err := resolveProfiled(suite.Name, normalizedMember{member: m, instructions: files[agent.Instructions.Path], cases: cases}, profiles)
	if err != nil {
		return "", err
	}
	if !equalFiles(files, expected) {
		return "", errors.New("unsupported profiled graph or incomplete coverage")
	}
	return suite.Name, nil
}

type profiledGraph struct {
	files     map[string][]byte
	artifact  suiteview.ArtifactIdentity
	selection Selection // frozen resolved tuple for detached declaration consumers
}

// RequireUnprofiled is a consumer gate, independent of declared runtime.id.
func (s *NativeSuite) RequireUnprofiled() error {
	if s == nil {
		return errors.New("native suite is required")
	}
	if s.Profile != nil || s.profiled != nil {
		return ErrProfileExecutionUnsupported
	}
	return nil
}

func LoadSelected(ctx context.Context, p *Prepared, selection Selection) (*NativeSuite, error) {
	files, err := verify(ctx, p)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, verificationFailed(err)
	}
	return loadSelectedFiles(ctx, files, recipeArtifactIdentity(p.Descriptor), selection)
}
func LoadArtifactSelected(ctx context.Context, artifact *suiteoras.Artifact, selection Selection) (*NativeSuite, error) {
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
	return loadSelectedFiles(ctx, files, recipeArtifactIdentity(artifact.Descriptor()), selection)
}
func loadSelectedFiles(ctx context.Context, files map[string][]byte, artifact suiteview.ArtifactIdentity, selection Selection) (*NativeSuite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !hasRuntimeProfiles(files) {
		if selection.RuntimeProfile != "" || selection.Platform != "" {
			return nil, errors.New("runtime-profile/platform selection is not applicable to legacy native suites")
		}
		if _, multi := files[multiNativePath]; multi {
			return nil, ErrMultiMemberUnsupported
		}
		s, err := loadNativeFiles(ctx, files, artifact)
		if err == nil && selection.Member != "" && selection.Member != s.MemberID {
			return nil, errors.New("unknown selected member")
		}
		return s, err
	}
	if selection.RuntimeProfile == "" {
		return nil, ErrProfileSelectionRequired
	}
	var root agentsuite.Suite
	if err := agentsuite.Unmarshal(files["agentsuite.json"], &root); err != nil {
		return nil, err
	}
	var matches []agentsuite.CompositionRef
	for _, ref := range root.Compositions {
		if ref.RuntimeProfile == selection.RuntimeProfile && (selection.Member == "" || ref.Agent == selection.Member) && (selection.Platform == "" || ref.Platform.String() == selection.Platform) {
			matches = append(matches, ref)
		}
	}
	if len(matches) == 0 {
		return nil, errors.New("unknown runtime profile/member/platform selection")
	}
	if len(matches) != 1 {
		return nil, errors.New("ambiguous profile selection; --platform is required")
	}
	ref := matches[0]
	recipe, err := suiteview.ProjectMember(ctx, files, ref.Agent)
	if err != nil {
		return nil, err
	}
	if len(root.Agents) != 1 || root.Agents[0].ID != ref.Agent {
		return nil, errors.New("profiled member does not match composition")
	}
	var agent agentsuite.Agent
	if err := agentsuite.Unmarshal(files[root.Agents[0].Path], &agent); err != nil {
		return nil, err
	}
	var composition agentsuite.Composition
	if err := agentsuite.Unmarshal(files[ref.Path], &composition); err != nil {
		return nil, err
	}
	var rp agentsuite.RuntimeProfile
	var profileDigest, buildDigest string
	for _, p := range root.RuntimeProfiles {
		if p.ID == ref.RuntimeProfile {
			if err := agentsuite.Unmarshal(files[p.Path], &rp); err != nil {
				return nil, err
			}
			profileDigest = p.Descriptor.Digest
		}
	}
	for _, p := range root.BuildProfiles {
		if p.ID == composition.BuildProfile {
			buildDigest = p.Descriptor.Digest
		}
	}
	cases, _, err := packagedCases(files, agent)
	if err != nil {
		return nil, err
	}
	// Retained graph bytes never alias mutable caller-visible selection fields.
	graph := &profiledGraph{files: cloneProfileFiles(files), artifact: artifact, selection: Selection{Member: ref.Agent, Platform: ref.Platform.String(), RuntimeProfile: ref.RuntimeProfile}}
	result := &NativeSuite{
		Digest: artifact.Digest, GraphDigest: recipe.Suite.GraphDigest, Name: recipe.Suite.Name,
		MemberID: recipe.Member.ID, Description: recipe.Description, Model: recipe.Model.Model,
		Instructions: recipe.Instructions,
		MemberDigest: recipe.Member.Descriptor.Digest, InstructionsDigest: recipe.InstructionsDescriptor.Digest,
		CompositionDigest: ref.Descriptor.Digest, BuildProfileDigest: buildDigest,
		Cases: cases, RequiredImages: []string{},
		Profile: &rp, ProfileDigest: profileDigest, Platform: ref.Platform, profiled: graph,
	}
	for i := range result.Cases {
		result.Cases[i].Bytes = bytes.Clone(result.Cases[i].Bytes)
	}
	if agent.Policy != nil {
		result.Policy = &NativePolicy{Path: agent.Policy.Path, Descriptor: agent.Policy.Descriptor, Bytes: bytes.Clone(files[agent.Policy.Path])}
	}
	return result, ctx.Err()
}
func cloneProfileFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for p, b := range files {
		out[p] = bytes.Clone(b)
	}
	return out
}

// ForCompositionDigest resolves only within this already verified same-S graph.
func (s *NativeSuite) ForCompositionDigest(ctx context.Context, digest string) (*NativeSuite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("native suite is required")
	}
	if s.profiled == nil {
		if digest != s.CompositionDigest {
			return nil, errors.New("foreign composition digest")
		}
		return s, nil
	}
	var root agentsuite.Suite
	if err := agentsuite.Unmarshal(s.profiled.files["agentsuite.json"], &root); err != nil {
		return nil, err
	}
	var matches []agentsuite.CompositionRef
	for _, ref := range root.Compositions {
		if ref.Descriptor.Digest == digest {
			matches = append(matches, ref)
		}
	}
	if len(matches) != 1 {
		return nil, errors.New("unknown or ambiguous composition digest")
	}
	ref := matches[0]
	if ref.Agent != s.MemberID {
		return nil, errors.New("composition member mismatch")
	}
	return loadSelectedFiles(ctx, s.profiled.files, s.profiled.artifact, Selection{Member: ref.Agent, Platform: ref.Platform.String(), RuntimeProfile: ref.RuntimeProfile})
}
