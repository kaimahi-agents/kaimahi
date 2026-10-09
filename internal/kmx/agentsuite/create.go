package agentsuite

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/gowebpki/jcs"
)

//go:embed testdata/incident-analyst/build-profiles/agentkit-v0.1.0.json
var defaultHTTPProfile []byte

type CreateRequest struct {
	Name         string
	Instructions string
	Inference    InferenceRequirements
}

// CreateHTTPSource writes a validated model-independent source suite into a new
// directory. Profile pins are explicit source content, never host discovery.
func CreateHTTPSource(destination string, request CreateRequest) error {
	if !identifierPattern.MatchString(request.Name) || request.Instructions == "" || len(request.Instructions) > 64<<10 {
		return errors.New("source requires a valid name and 1–65536 instruction bytes")
	}
	if err := request.Inference.Validate(); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("source destination already exists or is unreadable")
	}
	parent := filepath.Dir(destination)
	stage, err := os.MkdirTemp(parent, ".suite-source-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	write := func(name string, value any) (string, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		data, err = jcs.Transform(data)
		if err != nil {
			return "", err
		}
		return digestBytes(data), os.WriteFile(filepath.Join(stage, name), data, 0644)
	}
	if err := os.WriteFile(filepath.Join(stage, "instructions.txt"), []byte(request.Instructions), 0644); err != nil {
		return err
	}
	agent := Agent{SchemaVersion: SpecVersion, MediaType: MediaTypeAgent, ID: request.Name,
		Instructions: FileRef{Path: "instructions.txt", Digest: digestBytes([]byte(request.Instructions))},
		Model:        ModelRequirement{Protocol: "openai-compatible", Capabilities: &request.Inference}}
	agentDigest, err := write("agent.json", agent)
	if err != nil {
		return err
	}
	var profile BuildProfile
	if err := json.Unmarshal(defaultHTTPProfile, &profile); err != nil {
		return err
	}
	profile.Execution = &ExecutionContract{Configuration: AgentKitMountedConfig, Kind: ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []ExecutionInput{
		{Name: "agent-auth", Environment: "AGENTKIT_AUTH_TOKEN", Secret: true},
		{Name: "listen", Environment: "AGENTKIT_BIND", Secret: false},
	}}
	profileDigest, err := write("profile.json", profile)
	if err != nil {
		return err
	}
	platform := Platform{OS: "linux", Architecture: "amd64"}
	composition := Composition{SchemaVersion: SpecVersion, MediaType: MediaTypeComposition, Agent: request.Name, Platform: platform, BuildProfile: profile.ID, ToolProviders: []ResolvedToolProvider{}}
	compositionDigest, err := write("composition.json", composition)
	if err != nil {
		return err
	}
	catalogDigest, err := write("catalog.json", ToolProviderCatalog{SchemaVersion: SpecVersion, MediaType: MediaTypeToolProviderCatalog, ToolProviders: []ManifestRef{}})
	if err != nil {
		return err
	}
	suite := Suite{SchemaVersion: SpecVersion, MediaType: MediaTypeSuite, Name: request.Name,
		Agents:              []ManifestRef{{ID: request.Name, Path: "agent.json", Digest: agentDigest}},
		BuildProfiles:       []ManifestRef{{ID: profile.ID, Path: "profile.json", Digest: profileDigest}},
		Compositions:        []CompositionRef{{Agent: request.Name, Platform: platform, Path: "composition.json", Digest: compositionDigest}},
		ToolProviderCatalog: ManifestRef{Path: "catalog.json", Digest: catalogDigest}}
	if _, err := write("agentsuite.json", suite); err != nil {
		return err
	}
	if _, err := ValidatePath(stage); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}
