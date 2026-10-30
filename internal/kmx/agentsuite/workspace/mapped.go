package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

// MappedAgent is an explicit tool-free authoring boundary. It carries definitions,
// never local-preview outcomes or evidence. Cases retain their authored order.
type MappedAgent struct {
	Name, Description, Model string
	Instructions             []byte
	Cases                    []agentruntime.EvaluationCase
}

// CreateMappedOrka validates the complete mapped graph before publishing source
// using the same exclusive descriptor-last writer as ordinary workspace Create.
func CreateMappedOrka(ctx context.Context, root string, input MappedAgent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(input.Model) == "" || input.Model == "auto" || input.Model == "default" {
		return errors.New("mapping requires an exact deployment model")
	}
	if len(input.Cases) == 0 || len(input.Cases) > 32 {
		return errors.New("mapping requires 1-32 complete ordered cases")
	}
	m := sourceMember{ID: input.Name, Description: input.Description, Instructions: "instructions.md", Runtime: &sourceRuntime{ID: "orka"}}
	m.Model.Protocol, m.Model.Model = "openai-compatible", input.Model
	if err := validateMember(m, input.Instructions); err != nil {
		return err
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	for i, c := range input.Cases {
		if err := agentruntime.ValidateEvaluationCase(c); err != nil {
			return err
		}
		if seen[c.ID] {
			return errors.New("duplicate mapped case ID")
		}
		seen[c.ID] = true
		for _, check := range agentruntime.EvaluationCaseAssertions(c) {
			switch check.Type {
			case "contains", "notContains", "regex":
			default:
				return errors.New("mapping supports text assertions only; mock/tool/judge checks need an explicit native binding")
			}
		}
		if c.Runs != nil || c.MinPasses != nil {
			return errors.New("mapped preview cases must run once")
		}
		raw, err := yaml.Marshal(c)
		if err != nil {
			return err
		}
		// Use exactly the native parser to ensure the serialized source is accepted.
		if _, err = agentruntime.ParseEvaluationCase(raw); err != nil {
			return err
		}
		files[fmt.Sprintf("eval/%03d-%s.yaml", i+1, c.ID)] = raw
	}
	// Exercise existing public graph resolution before any destination writes.
	if _, err := resolve(input.Name, m, input.Instructions, defaultProfile, input.Cases); err != nil {
		return err
	}
	w := sourceWorkspace{SchemaVersion: 1, Name: input.Name, Member: "agents/" + input.Name + "/member.yaml", BuildProfile: "build-profiles/default.json"}
	wb, err := yaml.Marshal(w)
	if err != nil {
		return err
	}
	mb, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	files["workspace.yaml"], files[w.Member], files["agents/"+input.Name+"/instructions.md"], files[w.BuildProfile] = wb, mb, input.Instructions, defaultProfile
	return createSourceFiles(ctx, root, files)
}
