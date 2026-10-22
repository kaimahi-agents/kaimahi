package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	godigest "github.com/opencontainers/go-digest"
)

const rendererVersion = "kmx.native-orka/v2"
const nativePath = "metadata/kmx/native-orka.json"
const kagentNativePath = "metadata/kmx/native-kagent.json"
const kagentRendererVersion = "kmx.native-kagent/v2"
const agentsessionsNativePath = "metadata/kmx/native-agentsessions.json"
const agentsessionsRendererVersion = "kmx.native-agentsessions/v2"

// This contract binds deterministic system_prompt config and exact separately
// observed model identity. The public build profile is inert for plain chat.
const agentsessionsExecutionProfile = "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1"

// This immutable ID names the exact v0.10.3 Go contract qualified by
// app.kagentLiftProfile, scaffold.GenerateKagent's v1alpha2 declarative renderer,
// and the blocking final-text A2A answer contract. It is limited to tool-free
// OpenAI-compatible linux/amd64; it is not AgentKit's inert build profile or
// a claim that suite execution is implemented. Contract changes require a new ID.
const kagentExecutionProfile = "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1"

// nativeLayoutVersion is in the source fingerprint so old caches are not reused.
const nativeLayoutVersion = "kmx.native-suite-evals/v2"

const nativeRecordVersion = 2

var errPreviousNativeSuite = errors.New("packaged by an earlier kmx; rerun kmx eval and kmx push with this kmx")

// refusal is safe to show for untrusted suites.
type refusal struct{ err error }

func (r refusal) Error() string { return r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

// nativeRecord holds private execution metadata; cases live under evals/<member>/.
type nativeRecord struct {
	SchemaVersion    int             `json:"schemaVersion"`
	ExecutionMode    string          `json:"executionMode"`
	Member           string          `json:"member"`
	RendererVersion  string          `json:"rendererVersion"`
	RequiredImages   []string        `json:"requiredImages"`
	Runtime          *RuntimeProfile `json:"runtime,omitempty"`
	ExecutionProfile string          `json:"executionProfile,omitempty"`
}
type snapshot struct {
	name, fingerprint string
	files             map[string][]byte
	source            map[string]SourceFile
}

func entries(r *os.Root, name string, limit int) ([]os.DirEntry, error) {
	if err := confined(r, name); err != nil {
		return nil, err
	}
	info, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("source entries require a directory")
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("source directory exceeds entry limit")
	}
	return entries, nil
}
func caseName(name string) bool {
	return safeText(name) == nil && path.Base(name) == name && name != "." && name != ".." && !strings.Contains(name, "\\") && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"))
}
func loadSource(ctx context.Context, root string) (*snapshot, error) {
	return loadSourceMode(ctx, root, false)
}

func loadSourceMode(ctx context.Context, root string, flowMode bool) (*snapshot, error) {
	s, err := normalizeSourceMode(ctx, root, flowMode)
	if err != nil {
		return nil, err
	}
	var files map[string][]byte
	renderer, executionProfile := multiRendererVersion, ""
	var declaredRuntime *RuntimeProfile
	if s.workspace.SchemaVersion == 3 {
		m := s.members[0]
		files, err = resolveProfiled(s.workspace.Name, m, s.profiles)
		renderer = profiledLayoutVersion
	} else if s.workspace.SchemaVersion == 2 {
		files, err = resolveMulti(s.workspace.Name, s.members, s.profile)
	} else {
		m := s.members[0]
		files, err = resolve(s.workspace.Name, m.member, m.instructions, s.profile, m.cases)
		profile, profileErr := m.member.Runtime.profile()
		if profileErr != nil {
			return nil, profileErr
		}
		renderer = rendererVersion
		if profile.ID == "kagent" {
			renderer, executionProfile, declaredRuntime = kagentRendererVersion, kagentExecutionProfile, &profile
		} else if profile.ID == "agentsessions" {
			renderer, executionProfile, declaredRuntime = agentsessionsRendererVersion, agentsessionsExecutionProfile, &profile
		}
	}
	if err != nil {
		return nil, err
	}
	if flowMode {
		if err := attachFlow(files, s.flow); err != nil {
			return nil, err
		}
		if _, err := validateFlowFiles(ctx, files); err != nil {
			return nil, err
		}
	}
	// Bind the public case layout for every runtime and source schema.
	framed, err := suitecore.Marshal(struct {
		Renderer         string            `json:"renderer"`
		Layout           string            `json:"layout"`
		Files            map[string]string `json:"files"`
		Runtime          *RuntimeProfile   `json:"runtime,omitempty"`
		ExecutionProfile string            `json:"executionProfile,omitempty"`
	}{renderer, nativeLayoutVersion, s.selected, declaredRuntime, executionProfile})
	if err != nil {
		return nil, err
	}
	fingerprint, err := suitecore.Digest(framed)
	if err != nil {
		return nil, err
	}
	return &snapshot{name: s.workspace.Name, fingerprint: fingerprint, files: files, source: s.source}, nil
}

// authoredCases parses eval/*.yaml in file-name order (the evaluation order).
func authoredCases(cases map[string][]byte) ([]runtime.EvaluationCase, error) {
	parsed := make([]runtime.EvaluationCase, 0, len(cases))
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		if !caseName(name) {
			return nil, errors.New("unsupported case filename")
		}
		c, err := runtime.ParseEvaluationCase(cases[name])
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", name, err)
		}
		parsed = append(parsed, c)
	}
	return parsed, nil
}

// resolve builds the public graph bottom-up. Profile pins are declared build
// inputs required by that format, not images required by native execution.
func resolve(name string, m sourceMember, instructions, profile []byte, cases []runtime.EvaluationCase) (map[string][]byte, error) {
	if !scaffold.ValidSuiteName(name) || name == "." || name == ".." {
		return nil, errors.New("invalid workspace name")
	}
	if err := validateMember(m, instructions); err != nil {
		return nil, err
	}
	var bp, approved suitecore.BuildProfile
	if err := suitecore.Unmarshal(profile, &bp); err != nil {
		// Strict JSON diagnostics may quote decoded keys or trailing content.
		return nil, errors.New("invalid pinned build profile JSON")
	}
	if err := suitecore.Unmarshal(defaultProfile, &approved); err != nil {
		return nil, err
	}
	bpBytes, err := suitecore.Marshal(bp)
	if err != nil {
		return nil, err
	}
	approvedBytes, err := suitecore.Marshal(approved)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(bpBytes, approvedBytes) {
		return nil, errors.New("unsupported build profile; use the pinned default profile")
	}
	files, agentRef, catalogRef, err := resolveMemberGraph(m, instructions, cases)
	if err != nil {
		return nil, err
	}
	files["build-profiles/default.json"] = bpBytes
	put := func(name string, value any) (string, int64, error) {
		b, err := suitecore.Marshal(value)
		if err != nil {
			return "", 0, err
		}
		files[name] = b
		d, err := suitecore.Digest(b)
		return d, int64(len(b)), err
	}
	platform := suitecore.Platform{OS: "linux", Architecture: "amd64"}
	compositionPath := "compositions/" + m.ID + "-linux-amd64.json"
	compositionDigest, compositionSize, err := put(compositionPath, suitecore.Composition{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeComposition, Agent: m.ID, Platform: platform, BuildProfile: bp.ID, ToolProviders: []suitecore.ResolvedToolProvider{}})
	if err != nil {
		return nil, err
	}
	profileDigest, err := suitecore.Digest(bpBytes)
	if err != nil {
		return nil, err
	}
	_, _, err = put("agentsuite.json", suitecore.Suite{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeSuite, Name: name, Agents: []suitecore.ManifestRef{agentRef}, ToolProviderCatalog: catalogRef, Compositions: []suitecore.CompositionRef{{Agent: m.ID, Platform: platform, Path: compositionPath, Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeComposition, Digest: compositionDigest, Size: compositionSize}}}, BuildProfiles: []suitecore.ManifestRef{{ID: bp.ID, Path: "build-profiles/default.json", Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeBuildProfile, Digest: profileDigest, Size: int64(len(bpBytes))}}}})
	if err != nil {
		return nil, err
	}
	runtimeProfile, err := m.Runtime.profile()
	if err != nil {
		return nil, err
	}
	record := nativeRecord{SchemaVersion: nativeRecordVersion, ExecutionMode: "native-orka", Member: m.ID, RendererVersion: rendererVersion, RequiredImages: []string{}}
	metadataPath := nativePath
	if runtimeProfile.ID == "kagent" {
		metadataPath = kagentNativePath
		record.ExecutionMode, record.RendererVersion = "native-kagent", kagentRendererVersion
		record.Runtime, record.ExecutionProfile = &runtimeProfile, kagentExecutionProfile
	} else if runtimeProfile.ID == "agentsessions" {
		metadataPath = agentsessionsNativePath
		record.ExecutionMode, record.RendererVersion = "native-agentsessions", agentsessionsRendererVersion
		record.Runtime, record.ExecutionProfile = &runtimeProfile, agentsessionsExecutionProfile
	}
	_, _, err = put(metadataPath, record)
	return files, err
}

// resolveMemberGraph is shared by legacy and profiled packaging; content is authored once.
func resolveMemberGraph(m sourceMember, instructions []byte, cases []runtime.EvaluationCase) (map[string][]byte, suitecore.ManifestRef, suitecore.ManifestRef, error) {
	files := map[string][]byte{"instructions/" + m.ID + ".md": instructions}
	put := func(name string, value any) (string, int64, error) {
		b, err := suitecore.Marshal(value)
		if err != nil {
			return "", 0, err
		}
		files[name] = b
		digest, err := suitecore.Digest(b)
		if err != nil {
			return "", 0, err
		}
		return digest, int64(len(b)), nil
	}
	catalogPath := "tool-providers/catalog.json"
	catalogDigest, catalogSize, err := put(catalogPath, suitecore.ToolProviderCatalog{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeToolProviderCatalog, ToolProviders: []suitecore.ManifestRef{}})
	if err != nil {
		return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
	}
	if len(cases) == 0 || len(cases) > maxPackagedCases {
		return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, errors.New("one to 128 authored cases are required")
	}
	index := suitecore.EvaluationIndex{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeEvaluationIndex, Agent: m.ID, Cases: make([]suitecore.FileRef, 0, len(cases)), Limits: suitecore.EvaluationLimits{MaxCases: maxPackagedCases, MaxCaseBytes: maxPackagedCaseBytes}}
	ids := map[string]bool{}
	for _, c := range cases {
		// Case files are named by ID; avoid case-insensitive collisions.
		if ids[strings.ToLower(c.ID)] {
			return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, errors.New("duplicate evaluation case ID (IDs must differ by more than letter case)")
		}
		ids[strings.ToLower(c.ID)] = true
		b, err := publicCase(c)
		if err != nil {
			return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
		}
		p := evalCasePath(m.ID, c.ID)
		files[p] = b
		digest, err := suitecore.Digest(b)
		if err != nil {
			return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
		}
		index.Cases = append(index.Cases, suitecore.FileRef{Path: p, Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeEvaluationCase, Digest: digest, Size: int64(len(b))}})
	}
	indexDigest, indexSize, err := put(evalIndexPath(m.ID), index)
	if err != nil {
		return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
	}
	policyRef, err := bindMemberPolicy(files, m.ID, m.policyBytes)
	if err != nil {
		return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
	}
	agentPath := "agents/" + m.ID + ".json"
	agentDigest, agentSize, err := put(agentPath, suitecore.Agent{Policy: policyRef, SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeAgent, ID: m.ID, Description: m.Description, Instructions: suitecore.FileRef{Path: "instructions/" + m.ID + ".md", Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeInstructions, Digest: godigest.FromBytes(instructions).String(), Size: int64(len(instructions))}}, Model: suitecore.ModelRequirement{Protocol: m.Model.Protocol, Model: m.Model.Model}, Evals: []suitecore.FileRef{{Path: evalIndexPath(m.ID), Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeEvaluationIndex, Digest: indexDigest, Size: indexSize}}}})
	if err != nil {
		return nil, suitecore.ManifestRef{}, suitecore.ManifestRef{}, err
	}
	return files, suitecore.ManifestRef{ID: m.ID, Path: agentPath, Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeAgent, Digest: agentDigest, Size: agentSize}}, suitecore.ManifestRef{Path: catalogPath, Descriptor: suitecore.Descriptor{MediaType: suitecore.MediaTypeToolProviderCatalog, Digest: catalogDigest, Size: catalogSize}}, nil
}

// nativeProfile dispatches only by one complete private declaration. No target,
// live defaults or public graph fields can supply a missing runtime.
func nativeProfile(files map[string][]byte) (nativeRecord, RuntimeProfile, error) {
	var record nativeRecord
	_, orka := files[nativePath]
	_, kagent := files[kagentNativePath]
	_, sessions := files[agentsessionsNativePath]
	count := 0
	for _, present := range []bool{orka, kagent, sessions} {
		if present {
			count++
		}
	}
	if count != 1 {
		return record, RuntimeProfile{}, errors.New("exactly one native metadata profile is required")
	}
	metadataPath, mode, renderer := nativePath, "native-orka", rendererVersion
	profile := RuntimeProfile{ID: "orka"}
	if kagent {
		metadataPath, mode, renderer = kagentNativePath, "native-kagent", kagentRendererVersion
	} else if sessions {
		metadataPath, mode, renderer = agentsessionsNativePath, "native-agentsessions", agentsessionsRendererVersion
	}
	// Strict decoding would reject the v0.6.0 cases field first.
	var previous struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(files[metadataPath], &previous) == nil && previous.SchemaVersion == 1 {
		return record, RuntimeProfile{}, refusal{errPreviousNativeSuite}
	}
	if err := suitecore.Unmarshal(files[metadataPath], &record); err != nil {
		return record, RuntimeProfile{}, err
	}
	if record.SchemaVersion != nativeRecordVersion || record.ExecutionMode != mode || record.RendererVersion != renderer || record.RequiredImages == nil || len(record.RequiredImages) != 0 {
		return record, RuntimeProfile{}, errors.New("unsupported native metadata or required images")
	}
	if kagent {
		if record.Runtime == nil || *record.Runtime != (RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}) || record.ExecutionProfile != kagentExecutionProfile {
			return record, RuntimeProfile{}, errors.New("unsupported kagent execution profile")
		}
		profile = *record.Runtime
	} else if sessions {
		if record.Runtime == nil || *record.Runtime != (RuntimeProfile{ID: "agentsessions", Version: AgentSessionsVersion, Engine: "chat"}) || record.ExecutionProfile != agentsessionsExecutionProfile {
			return record, RuntimeProfile{}, errors.New("unsupported agentsessions execution profile")
		}
		profile = *record.Runtime
	} else if record.Runtime != nil || record.ExecutionProfile != "" {
		return record, RuntimeProfile{}, errors.New("unsupported Orka metadata")
	}
	return record, profile, nil
}

// validateNative regenerates the supported graph and requires exact, full
// allowlist coverage, so public-format extensions cannot silently disappear.
func validateNative(files map[string][]byte) (string, error) {
	if err := nativeBounds(files); err != nil {
		return "", err
	}
	if hasRuntimeProfiles(files) {
		return validateProfiled(files)
	}
	if _, multi := files[multiNativePath]; multi {
		return validateMulti(files)
	}
	var suite suitecore.Suite
	if err := suitecore.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return "", err
	}
	record, profile, err := nativeProfile(files)
	if err != nil {
		return "", err
	}
	var agent suitecore.Agent
	if err := suitecore.Unmarshal(files["agents/"+record.Member+".json"], &agent); err != nil {
		return "", err
	}
	m := sourceMember{ID: agent.ID, Description: agent.Description, Runtime: &sourceRuntime{ID: profile.ID}}
	if profile.ID != "orka" {
		m.Runtime.Version, m.Runtime.Engine = &profile.Version, &profile.Engine
	}
	m.Model.Protocol, m.Model.Model = agent.Model.Protocol, agent.Model.Model
	_, cases, err := packagedCases(files, agent)
	if err != nil {
		return "", refusal{err}
	}
	m.policyBytes, err = readMemberPolicy(files, agent, profile)
	if err != nil {
		return "", err
	}
	expected, err := resolve(suite.Name, m, files["instructions/"+m.ID+".md"], files["build-profiles/default.json"], cases)
	if err != nil {
		return "", err
	}
	if !equalFiles(expected, files) {
		return "", errors.New("unsupported native graph or incomplete case coverage")
	}
	return suite.Name, nil
}

const (
	maxPackagedCases = 128
	// Matches the 1 MiB per-file artifact read limit.
	maxPackagedCaseBytes = 1 << 20
)

func evalIndexPath(member string) string { return "evals/" + member + "/index.json" }
func evalCasePath(member, id string) string {
	return "evals/" + member + "/cases/" + id + ".json"
}

// publicCase converts an authored case; expectContains keeps its expectContains.N IDs.
func publicCase(c runtime.EvaluationCase) ([]byte, error) {
	out := suitecore.EvaluationCase{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeEvaluationCase, ID: c.ID, Input: c.Input, Runs: c.Runs, MinPasses: c.MinPasses, Assertions: []suitecore.EvaluationAssertion{}}
	for _, a := range runtime.EvaluationCaseAssertions(c) {
		if strings.ContainsAny(a.Value+a.Tool, "\u2028\u2029") {
			return nil, fmt.Errorf("case %s: assertion %s must not contain Unicode line or paragraph separators", c.ID, a.ID)
		}
		out.Assertions = append(out.Assertions, suitecore.EvaluationAssertion{ID: a.ID, Type: a.Type, Value: a.Value, Pattern: a.Pattern, Tool: a.Tool, Rubric: a.Rubric, MinScore: a.MinScore, JudgeModel: a.JudgeModel, JudgeVersion: a.JudgeVersion})
	}
	b, err := suitecore.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("case %s: %w", c.ID, err)
	}
	if len(b) > maxPackagedCaseBytes {
		return nil, fmt.Errorf("case %s exceeds 1 MiB when packaged", c.ID)
	}
	return b, nil
}

// DecodePackagedCase converts a packaged case back; unsupported features are refused, not ignored.
func DecodePackagedCase(b []byte) (runtime.EvaluationCase, error) {
	var pc suitecore.EvaluationCase
	if err := suitecore.Unmarshal(b, &pc); err != nil {
		return runtime.EvaluationCase{}, errors.New("invalid packaged evaluation case JSON")
	}
	if pc.SchemaVersion != suitecore.SpecVersion || pc.MediaType != suitecore.MediaTypeEvaluationCase {
		return runtime.EvaluationCase{}, errors.New("unsupported packaged evaluation case schemaVersion or mediaType")
	}
	c := runtime.EvaluationCase{ID: pc.ID, Input: pc.Input, Runs: pc.Runs, MinPasses: pc.MinPasses}
	for _, a := range pc.Assertions {
		// critical defaults to true, which is how KMX evaluates every assertion.
		if a.Critical != nil && !*a.Critical {
			return runtime.EvaluationCase{}, errors.New("packaged evaluation assertion is non-critical; KMX evaluates every assertion as critical")
		}
		if strings.HasPrefix(a.ID, "expectContains.") {
			if len(c.Assertions) != 0 || a.ID != fmt.Sprintf("expectContains.%d", len(c.ExpectContains)) || a.Type != "contains" {
				return runtime.EvaluationCase{}, errors.New("packaged expectContains assertions must be leading, ordered contains assertions")
			}
			c.ExpectContains = append(c.ExpectContains, a.Value)
			continue
		}
		c.Assertions = append(c.Assertions, runtime.EvaluationAssertion{ID: a.ID, Type: a.Type, Value: a.Value, Pattern: a.Pattern, Tool: a.Tool, Rubric: a.Rubric, MinScore: a.MinScore, JudgeModel: a.JudgeModel, JudgeVersion: a.JudgeVersion})
	}
	if err := runtime.ValidateEvaluationCase(c); err != nil {
		return runtime.EvaluationCase{}, err
	}
	return c, nil
}

// packagedCases reads an agent's evaluation index in order.
func packagedCases(files map[string][]byte, agent suitecore.Agent) ([]PackagedCase, []runtime.EvaluationCase, error) {
	if len(agent.Evals) != 1 || agent.Evals[0].Path != evalIndexPath(agent.ID) {
		return nil, nil, errors.New("native member requires exactly one evaluation index")
	}
	var index suitecore.EvaluationIndex
	if err := suitecore.Unmarshal(files[agent.Evals[0].Path], &index); err != nil {
		return nil, nil, errors.New("invalid packaged evaluation index")
	}
	if index.Agent != agent.ID {
		return nil, nil, errors.New("packaged evaluation index does not match its member")
	}
	if len(index.Cases) == 0 || len(index.Cases) > maxPackagedCases {
		return nil, nil, errors.New("packaged evaluation index must list 1 to 128 cases")
	}
	packaged := make([]PackagedCase, 0, len(index.Cases))
	cases := make([]runtime.EvaluationCase, 0, len(index.Cases))
	for _, ref := range index.Cases {
		b, ok := files[ref.Path]
		if !ok {
			return nil, nil, errors.New("packaged evaluation case is missing")
		}
		c, err := DecodePackagedCase(b)
		if err != nil {
			return nil, nil, err
		}
		if ref.Path != evalCasePath(agent.ID, c.ID) {
			return nil, nil, errors.New("packaged evaluation case path does not match its id")
		}
		packaged = append(packaged, PackagedCase{Path: ref.Path, Digest: godigest.FromBytes(b).String(), Bytes: b})
		cases = append(cases, c)
	}
	return packaged, cases, nil
}
