package agentfile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type runProfile struct {
	SchemaVersion  int                    `json:"schemaVersion"`
	Kind           string                 `json:"kind"`
	SourceRevision string                 `json:"sourceRevision"`
	AgentID        string                 `json:"agentId"`
	Instructions   runProfileInstructions `json:"instructions"`
	Model          runProfileModel        `json:"model"`
	Examples       []runProfileExample    `json:"examples"`
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

// ExportRunProfile loads one canonical agent file and returns its deterministic,
// content-bearing v1 JSON projection, including a final newline. It preserves
// authored models and ordered examples, marked not_run on the target. It reads
// no configuration and grants no evaluation, package or deployment authority.
// Load's bounded-file and non-hostile-filesystem guarantees apply unchanged.
func ExportRunProfile(filename string) ([]byte, error) {
	agent, err := Load(filename)
	if err != nil {
		return nil, err
	}
	profile := runProfile{
		SchemaVersion: 1, Kind: "kmx.agent-run-profile",
		SourceRevision: agent.Revision, AgentID: agent.Identity.ID,
		Instructions: runProfileInstructions{agent.Instructions, runProfileHash([]byte(agent.Instructions))},
		Model:        runProfileModel{agent.Model, runProfileHash([]byte(agent.Model))},
		Examples:     make([]runProfileExample, 0, len(agent.Examples)),
	}
	for _, example := range agent.Examples {
		definition := runProfileExampleDefinition{example.ID, example.Input, example.Assertions}
		preimage, err := json.Marshal(definition)
		if err != nil {
			return nil, errors.New("cannot encode run profile example")
		}
		profile.Examples = append(profile.Examples, runProfileExample{definition, runProfileHash(preimage), "not_run"})
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil || len(data)+1 > 2*1024*1024 {
		return nil, errors.New("cannot encode bounded run profile")
	}
	return append(data, '\n'), nil
}

func runProfileHash(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}
