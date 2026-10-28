package agentfile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type runProfile struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	Kind             string                 `json:"kind"`
	SourceRevision   string                 `json:"sourceRevision"`
	SnapshotRevision string                 `json:"snapshotRevision,omitempty"`
	TestsSource      *runProfileTestsSource `json:"testsSource,omitempty"`
	AgentID          string                 `json:"agentId"`
	Instructions     runProfileInstructions `json:"instructions"`
	Model            runProfileModel        `json:"model"`
	Examples         []runProfileExample    `json:"examples"`
}

type runProfileTestsSource struct {
	State        string `json:"state"`
	Path         string `json:"path"`
	Revision     string `json:"revision"`
	SelectionKey string `json:"selectionKey"`
}

type runProfileInstructions struct {
	Text   string `json:"text"`
	SHA256 string `json:"sha256"`
}

type runProfileModel struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// Field order and encoding/json's default HTML escaping are part of the v1
// example-hash preimage, independently of the enclosing pretty-printed profile.
type runProfileExampleDefinition struct {
	ID         string                        `json:"id"`
	Input      string                        `json:"input"`
	Assertions []runtime.EvaluationAssertion `json:"assertions"`
}

type runProfileExample struct {
	runProfileExampleDefinition
	SHA256       string `json:"sha256"`
	TargetStatus string `json:"targetStatus"`
}

// ExportRunProfile exports the default selected sources; see ExportSelectedRunProfile.
func ExportRunProfile(filename string) ([]byte, error) {
	return ExportSelectedRunProfile(filename, "")
}

// ExportSelectedRunProfile loads one checked pair and returns deterministic,
// content-bearing JSON with a final newline. External tests use v2, binding the
// physical selected path and snapshot; legacy inline profiles retain v1 bytes.
// Authored models and ordered cases are preserved, marked not_run on the target.
// It reads no configuration and grants no execution, package or write authority.
// LoadPair's bounded-source and non-hostile-filesystem guarantees apply.
func ExportSelectedRunProfile(filename, testsOverride string) ([]byte, error) {
	pair, err := LoadPair(filename, testsOverride)
	if err != nil {
		return nil, err
	}
	// Only this encoding copy gets selected cases, never the source agent or a runner.
	agent := *pair.Agent
	if pair.Tests.Suite != nil {
		agent.Examples = pair.Tests.Suite.Examples
	}
	examplesFile := filename
	if pair.Tests.State == "external" {
		examplesFile = pair.Tests.Selection.Path
	}
	profile, err := runProfileFromAgent(filename, examplesFile, &agent)
	if err != nil {
		return nil, err
	}
	if pair.Tests.State == "external" {
		profile.SchemaVersion = 2
		profile.SnapshotRevision = pair.SnapshotRevision
		profile.TestsSource = &runProfileTestsSource{pair.Tests.State, pair.Tests.Selection.Path, pair.Tests.Revision, pair.Tests.Selection.Key}
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil || len(data)+1 > 2*1024*1024 {
		return nil, errors.New("cannot encode bounded run profile")
	}
	return append(data, '\n'), nil
}

func runProfileFromAgent(filename, examplesFile string, agent *Agent) (*runProfile, error) {
	// Offline export preserves authored choices, not preview-time defaults.
	if agent.Model == "" {
		return nil, refuse(filename, 1, 1, "Model.Preferred", "add nonblank required fields in the canonical charter format")
	}
	// A profile hands examples to a separate runtime; zero tests is no evidence.
	if len(agent.Examples) == 0 {
		loc := agent.Locations["Examples"]
		if examplesFile != filename {
			loc = Location{examplesFile, 1, 1}
		}
		if loc.Line == 0 {
			loc = Location{filename, 1, 1}
		}
		return nil, refuse(loc.File, loc.Line, loc.Column, "Examples", "add at least one example before exporting a run profile")
	}
	for _, example := range agent.Examples {
		unsupported := example.Mocks != nil
		for _, assertion := range example.Assertions {
			unsupported = unsupported || assertion.Type == "toolCalled" || assertion.Type == "toolNotCalled"
		}
		if unsupported {
			return nil, refuse(examplesFile, example.Location.Line, example.Location.Column, "Examples", "run profiles cannot preserve tool fixtures or tool checks")
		}
	}
	profile := runProfile{
		SchemaVersion: 1, Kind: "kmx.agent-run-profile",
		SourceRevision: agent.Revision, AgentID: agent.Identity.ID,
		Instructions: runProfileInstructions{agent.Instructions, runProfileHash([]byte(agent.Instructions))},
		Model:        runProfileModel{agent.Model, runProfileHash([]byte(agent.Model))},
		Examples:     make([]runProfileExample, 0, len(agent.Examples)),
	}
	for _, example := range agent.Examples {
		// Export cannot choose an execution model; preserve strict refusal for
		// unbound judge definitions rather than inventing an authored pin.
		if runtime.ValidateEvaluationAssertions(example.Assertions) != nil {
			return nil, refuse(examplesFile, example.Location.Line, example.Location.Column, "Examples", "pin judgeModel before exporting a run profile")
		}
		definition := runProfileExampleDefinition{example.ID, example.Input, example.Assertions}
		preimage, err := json.Marshal(definition)
		if err != nil {
			return nil, errors.New("cannot encode run profile example")
		}
		profile.Examples = append(profile.Examples, runProfileExample{definition, runProfileHash(preimage), "not_run"})
	}
	return &profile, nil
}

func runProfileHash(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}
