package workspace

import (
	"bytes"
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"slices"
)

// SourceBundle carries exact, non-confidential originals; it is not safe logging data.
type SourceBundle struct {
	Format        string
	SchemaVersion int
	Agent, Tests  []byte
}

// MappedInput is a complete supported projection from a trusted source adapter.
// The adapter owns source parsing and freshness; this compiler infers neither.
type MappedInput struct {
	SuiteName, MemberID, Description, Model, Protocol string
	Runtime                                           RuntimeProfile
	Instructions                                      []byte
	Cases                                             []runtime.EvaluationCase
	RequiredFeatures, UnsupportedFields               []string
	Sources                                           SourceBundle
}

type mappedInput struct{ value MappedInput }

var errMappedInput = errors.New("mapped native input is unsupported or invalid")

func freezeMapped(input MappedInput) (*mappedInput, error) {
	if input.Runtime != (RuntimeProfile{ID: "orka"}) || len(input.RequiredFeatures) != 0 || len(input.UnsupportedFields) != 0 || input.Sources.Format != "kmx.orka-agent-markdown" || input.Sources.SchemaVersion != 1 || len(input.Sources.Agent) == 0 || len(input.Sources.Tests) == 0 || len(input.Sources.Agent) > 256<<10 || len(input.Sources.Tests) > 256<<10 {
		return nil, errMappedInput
	}
	input.Instructions = bytes.Clone(input.Instructions)
	input.Sources.Agent = bytes.Clone(input.Sources.Agent)
	input.Sources.Tests = bytes.Clone(input.Sources.Tests)
	input.RequiredFeatures = slices.Clone(input.RequiredFeatures)
	input.UnsupportedFields = slices.Clone(input.UnsupportedFields)
	input.Cases = slices.Clone(input.Cases)
	for i := range input.Cases {
		c := &input.Cases[i]
		c.ExpectContains = slices.Clone(c.ExpectContains)
		c.Assertions = slices.Clone(c.Assertions)
		c.Runs = copyMappedInt(c.Runs)
		c.MinPasses = copyMappedInt(c.MinPasses)
		for j := range c.Assertions {
			a := &c.Assertions[j]
			a.MinScore = copyMappedInt(a.MinScore)
			if a.Type == "toolCalled" || a.Type == "toolNotCalled" {
				return nil, errMappedInput
			}
		}
		if runtime.ValidateEvaluationCase(*c) != nil {
			return nil, errMappedInput
		}
	}
	frozen := &mappedInput{value: input}
	if _, err := resolveMappedBase(context.Background(), frozen); err != nil {
		return nil, errMappedInput
	}
	return frozen, nil
}

func copyMappedInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func resolveMappedBase(ctx context.Context, input *mappedInput) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, errMappedInput
	}
	i := input.value
	member := sourceMember{ID: i.MemberID, Description: i.Description, Runtime: &sourceRuntime{ID: "orka"}}
	member.Model.Protocol = i.Protocol
	member.Model.Model = i.Model
	files, err := resolve(i.SuiteName, member, i.Instructions, defaultProfile, i.Cases)
	if err != nil {
		return nil, errMappedInput
	}
	if nativeBounds(files) != nil {
		return nil, errMappedInput
	}
	return files, nil
}
