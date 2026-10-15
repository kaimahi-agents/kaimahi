package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
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

type caseRef struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type nativeRecord struct {
	SchemaVersion    int             `json:"schemaVersion"`
	ExecutionMode    string          `json:"executionMode"`
	Member           string          `json:"member"`
	RendererVersion  string          `json:"rendererVersion"`
	RequiredImages   []string        `json:"requiredImages"`
	Cases            []caseRef       `json:"cases"`
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := safeAbsolute(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	selected := map[string]string{}
	source := map[string]SourceFile{}
	read := func(name string) ([]byte, error) {
		b, err := readBounded(r, name)
		if err != nil {
			return nil, err
		}
		info, err := r.Lstat(name)
		if err != nil {
			return nil, err
		}
		selected[name] = godigest.FromBytes(b).String()
		source[name] = SourceFile{Data: b, Mode: info.Mode()}
		return b, nil
	}
	wb, err := read("workspace.yaml")
	if err != nil {
		return nil, err
	}
	w, err := parseSourceWorkspace(wb)
	if err != nil {
		return nil, err
	}
	mb, err := read(w.Member)
	if err != nil {
		return nil, err
	}
	var m sourceMember
	if err := strictYAML(mb, &m); err != nil {
		return nil, fmt.Errorf("member: %w", err)
	}
	if w.Member != "agents/"+m.ID+"/member.yaml" {
		return nil, errors.New("workspace must select one agents/ID/member.yaml")
	}
	agents, err := entries(r, "agents", 1)
	if err != nil {
		return nil, err
	}
	if len(agents) != 1 || agents[0].Name() != m.ID || !agents[0].IsDir() {
		return nil, errors.New("workspace must contain exactly one member")
	}
	// Check before joining: cleaning an authored '..' must not conceal an escape.
	if !fs.ValidPath(m.Instructions) || strings.Contains(m.Instructions, "\\") {
		return nil, errors.New("instructions path must be member-relative")
	}
	instructions, err := read(path.Dir(w.Member) + "/" + m.Instructions)
	if err != nil {
		return nil, err
	}
	if err := validateMember(m, instructions); err != nil {
		return nil, err
	}
	profile, err := read(w.BuildProfile)
	if err != nil {
		return nil, err
	}
	caseEntries, err := entries(r, "eval", 128)
	if err != nil {
		return nil, err
	}
	cases := map[string][]byte{}
	total := len(wb) + len(mb) + len(instructions) + len(profile)
	for _, entry := range caseEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !caseName(entry.Name()) || !entry.Type().IsRegular() {
			return nil, errors.New("eval entries must be regular YAML files without subdirectories")
		}
		b, err := read("eval/" + entry.Name())
		if err != nil {
			return nil, err
		}
		total += len(b)
		if total > 8<<20 {
			return nil, errors.New("workspace source exceeds 8 MiB")
		}
		cases[entry.Name()] = b
	}
	files, err := resolve(w.Name, m, instructions, profile, cases)
	if err != nil {
		return nil, err
	}
	runtimeProfile, err := m.Runtime.profile()
	if err != nil {
		return nil, err
	}
	renderer, executionProfile := rendererVersion, ""
	var declaredRuntime *RuntimeProfile
	if runtimeProfile.ID == "kagent" {
		renderer, executionProfile, declaredRuntime = kagentRendererVersion, kagentExecutionProfile, &runtimeProfile
	} else if runtimeProfile.ID == "agentsessions" {
		renderer, executionProfile, declaredRuntime = agentsessionsRendererVersion, agentsessionsExecutionProfile, &runtimeProfile
	}
	framed, err := spec.Marshal(struct {
		Renderer         string            `json:"renderer"`
		Files            map[string]string `json:"files"`
		Runtime          *RuntimeProfile   `json:"runtime,omitempty"`
		ExecutionProfile string            `json:"executionProfile,omitempty"`
	}{renderer, selected, declaredRuntime, executionProfile})
	if err != nil {
		return nil, err
	}
	fingerprint, err := spec.Digest(framed)
	if err != nil {
		return nil, err
	}
	return &snapshot{name: w.Name, fingerprint: fingerprint, files: files, source: source}, nil
}

// resolve builds the public graph bottom-up. Profile pins are declared build
// inputs required by that format, not images required by native execution.
func resolve(name string, m sourceMember, instructions, profile []byte, cases map[string][]byte) (map[string][]byte, error) {
	if !namePattern.MatchString(name) || name == "." || name == ".." {
		return nil, errors.New("invalid workspace name")
	}
	if err := validateMember(m, instructions); err != nil {
		return nil, err
	}
	var bp, approved spec.BuildProfile
	if err := spec.Unmarshal(profile, &bp); err != nil {
		// Strict JSON diagnostics may quote decoded keys or trailing content.
		return nil, errors.New("invalid pinned build profile JSON")
	}
	if err := spec.Unmarshal(defaultProfile, &approved); err != nil {
		return nil, err
	}
	bpBytes, err := spec.Marshal(bp)
	if err != nil {
		return nil, err
	}
	approvedBytes, err := spec.Marshal(approved)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(bpBytes, approvedBytes) {
		return nil, errors.New("unsupported build profile; use the pinned default profile")
	}
	files := map[string][]byte{"instructions/" + m.ID + ".md": instructions, "build-profiles/default.json": bpBytes}
	put := func(name string, value any) (string, int64, error) {
		b, err := spec.Marshal(value)
		if err != nil {
			return "", 0, err
		}
		files[name] = b
		digest, err := spec.Digest(b)
		if err != nil {
			return "", 0, err
		}
		return digest, int64(len(b)), nil
	}
	catalogPath := "tool-providers/catalog.json"
	catalogDigest, catalogSize, err := put(catalogPath, spec.ToolProviderCatalog{SchemaVersion: spec.SpecVersion, MediaType: spec.MediaTypeToolProviderCatalog, ToolProviders: []spec.ManifestRef{}})
	if err != nil {
		return nil, err
	}
	agentPath := "agents/" + m.ID + ".json"
	agentDigest, agentSize, err := put(agentPath, spec.Agent{SchemaVersion: spec.SpecVersion, MediaType: spec.MediaTypeAgent, ID: m.ID, Description: m.Description, Instructions: spec.FileRef{Path: "instructions/" + m.ID + ".md", Descriptor: spec.Descriptor{MediaType: spec.MediaTypeInstructions, Digest: godigest.FromBytes(instructions).String(), Size: int64(len(instructions))}}, Model: spec.ModelRequirement{Protocol: m.Model.Protocol, Model: m.Model.Model}})
	if err != nil {
		return nil, err
	}
	platform := spec.Platform{OS: "linux", Architecture: "amd64"}
	compositionPath := "compositions/" + m.ID + "-linux-amd64.json"
	compositionDigest, compositionSize, err := put(compositionPath, spec.Composition{SchemaVersion: spec.SpecVersion, MediaType: spec.MediaTypeComposition, Agent: m.ID, Platform: platform, BuildProfile: bp.ID, ToolProviders: []spec.ResolvedToolProvider{}})
	if err != nil {
		return nil, err
	}
	profileDigest, err := spec.Digest(bpBytes)
	if err != nil {
		return nil, err
	}
	_, _, err = put("agentsuite.json", spec.Suite{SchemaVersion: spec.SpecVersion, MediaType: spec.MediaTypeSuite, Name: name, Agents: []spec.ManifestRef{{ID: m.ID, Path: agentPath, Descriptor: spec.Descriptor{MediaType: spec.MediaTypeAgent, Digest: agentDigest, Size: agentSize}}}, ToolProviderCatalog: spec.ManifestRef{Path: catalogPath, Descriptor: spec.Descriptor{MediaType: spec.MediaTypeToolProviderCatalog, Digest: catalogDigest, Size: catalogSize}}, Compositions: []spec.CompositionRef{{Agent: m.ID, Platform: platform, Path: compositionPath, Descriptor: spec.Descriptor{MediaType: spec.MediaTypeComposition, Digest: compositionDigest, Size: compositionSize}}}, BuildProfiles: []spec.ManifestRef{{ID: bp.ID, Path: "build-profiles/default.json", Descriptor: spec.Descriptor{MediaType: spec.MediaTypeBuildProfile, Digest: profileDigest, Size: int64(len(bpBytes))}}}})
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 || len(cases) > 128 {
		return nil, errors.New("one to 128 authored cases are required")
	}
	runtimeProfile, err := m.Runtime.profile()
	if err != nil {
		return nil, err
	}
	record := nativeRecord{SchemaVersion: 1, ExecutionMode: "native-orka", Member: m.ID, RendererVersion: rendererVersion, RequiredImages: []string{}, Cases: []caseRef{}}
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
	ids := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		if !caseName(name) {
			return nil, errors.New("unsupported case filename")
		}
		c, err := runtime.ParseEvaluationCase(cases[name])
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", name, err)
		}
		if ids[c.ID] {
			return nil, errors.New("duplicate evaluation case ID")
		}
		ids[c.ID] = true
		p := "metadata/kmx/eval/" + name
		files[p] = cases[name]
		record.Cases = append(record.Cases, caseRef{p, godigest.FromBytes(cases[name]).String()})
	}
	_, _, err = put(metadataPath, record)
	return files, err
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
	if err := spec.Unmarshal(files[metadataPath], &record); err != nil {
		return record, RuntimeProfile{}, err
	}
	if record.SchemaVersion != 1 || record.ExecutionMode != mode || record.RendererVersion != renderer || record.RequiredImages == nil || len(record.RequiredImages) != 0 {
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
	var suite spec.Suite
	if err := spec.Unmarshal(files["agentsuite.json"], &suite); err != nil {
		return "", err
	}
	record, profile, err := nativeProfile(files)
	if err != nil {
		return "", err
	}
	var agent spec.Agent
	if err := spec.Unmarshal(files["agents/"+record.Member+".json"], &agent); err != nil {
		return "", err
	}
	m := sourceMember{ID: agent.ID, Description: agent.Description, Runtime: &sourceRuntime{ID: profile.ID}}
	if profile.ID != "orka" {
		m.Runtime.Version, m.Runtime.Engine = &profile.Version, &profile.Engine
	}
	m.Model.Protocol, m.Model.Model = agent.Model.Protocol, agent.Model.Model
	cases := map[string][]byte{}
	for _, c := range record.Cases {
		name := strings.TrimPrefix(c.Path, "metadata/kmx/eval/")
		b, ok := files[c.Path]
		if !ok || !caseName(name) || cases[name] != nil || godigest.FromBytes(b).String() != c.Digest {
			return "", errors.New("native case path/digest/coverage mismatch")
		}
		cases[name] = b
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
