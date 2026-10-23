package agentfile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const RunProfileVersion = "kmx.run-profile/v1"

type runProfileExampleBody struct {
	ID         string                        `json:"id"`
	Input      string                        `json:"input"`
	Assertions []runtime.EvaluationAssertion `json:"assertions"`
}
type runProfileExample struct {
	runProfileExampleBody
	SHA256 string `json:"sha256"`
}
type runProfile struct {
	SchemaVersion       string              `json:"schemaVersion"`
	AgentID             string              `json:"agentID"`
	SourceRevision      string              `json:"sourceRevision"`
	AuthoredModel       string              `json:"authoredModel"`
	Instructions        string              `json:"instructions"`
	InstructionsSHA256  string              `json:"instructionsSHA256"`
	SessionConfig       json.RawMessage     `json:"sessionConfig"`
	SessionConfigSHA256 string              `json:"sessionConfigSHA256"`
	Examples            []runProfileExample `json:"examples"`
	ExamplesSHA256      string              `json:"examplesSHA256"`
	TargetExamples      string              `json:"targetExamples"`
}

// RunProfileSummary is safe metadata, not the content-bearing profile or passing evidence.
type RunProfileSummary struct {
	SchemaVersion       string `json:"schemaVersion"`
	ProfileSHA256       string `json:"profileSHA256"`
	SourceRevision      string `json:"sourceRevision"`
	AuthoredModel       string `json:"authoredModel"`
	InstructionsSHA256  string `json:"instructionsSHA256"`
	SessionConfigSHA256 string `json:"sessionConfigSHA256"`
	ExamplesSHA256      string `json:"examplesSHA256"`
	ExampleCount        int    `json:"exampleCount"`
	TargetExamples      string `json:"targetExamples"`
}

func runProfileDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

// FreezeRunProfile serializes a snapshot already accepted by canonical Parse/Load.
// Hashes bind kmx's JSON bytes, not original YAML case bytes. It performs no IO,
// model selection, example execution or runtime admission; locations are excluded.
func FreezeRunProfile(a *Agent) ([]byte, RunProfileSummary, error) {
	var zero RunProfileSummary
	if a == nil {
		return nil, zero, errors.New("run profile requires a canonical parsed snapshot")
	}
	config, err := json.Marshal(struct {
		SystemPrompt string `json:"system_prompt"`
	}{a.Instructions})
	if err != nil {
		return nil, zero, err
	}
	examples := make([]runProfileExample, 0, len(a.Examples))
	for _, e := range a.Examples {
		body := runProfileExampleBody{e.ID, e.Input, e.Assertions}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, zero, err
		}
		examples = append(examples, runProfileExample{body, runProfileDigest(raw)})
	}
	exampleBytes, err := json.Marshal(examples)
	if err != nil {
		return nil, zero, err
	}
	p := runProfile{SchemaVersion: RunProfileVersion, AgentID: a.Identity.ID, SourceRevision: a.Revision, AuthoredModel: a.Model, Instructions: a.Instructions, InstructionsSHA256: runProfileDigest([]byte(a.Instructions)), SessionConfig: config, SessionConfigSHA256: runProfileDigest(config), Examples: examples, ExamplesSHA256: runProfileDigest(exampleBytes), TargetExamples: "not-run"}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, zero, err
	}
	raw = append(raw, '\n')
	if len(raw) > 4<<20 {
		return nil, zero, errors.New("run profile exceeds 4 MiB")
	}
	return raw, RunProfileSummary{SchemaVersion: p.SchemaVersion, ProfileSHA256: runProfileDigest(raw), SourceRevision: p.SourceRevision, AuthoredModel: p.AuthoredModel, InstructionsSHA256: p.InstructionsSHA256, SessionConfigSHA256: p.SessionConfigSHA256, ExamplesSHA256: p.ExamplesSHA256, ExampleCount: len(examples), TargetExamples: p.TargetExamples}, nil
}
