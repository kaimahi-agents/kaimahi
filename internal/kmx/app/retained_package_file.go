package app

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

var errRetainedSourceChanged = errors.New("source_changed")
var errRetainedUnsupported = errors.New("retained package input is unsupported")

type retainedFileBinding struct {
	Agent, Tests, Revision, AgentRevision, TestsRevision string
	Explicit                                             bool
}
type retainedFileSnapshot struct {
	pair    *agentfile.OrkaSourcePair
	input   workspace.MappedInput
	binding retainedFileBinding
}

func loadRetainedFile(ctx context.Context, file, tests, expectedR string) (*retainedFileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expectedR == "" {
		return nil, errRetainedSourceChanged
	}
	pair, err := agentfile.LoadOrkaPair(file, tests)
	if err != nil {
		return nil, err
	}
	if pair.SnapshotRevision != expectedR {
		return nil, errRetainedSourceChanged
	}
	a := pair.Agent
	if len(pair.Unsupported) != 0 || a.Soul != nil || len(a.Skills) != 0 || len(a.Tools) != 0 || pair.Tests.Suite == nil || secretshapes.Match(string(a.Source)) != nil || secretshapes.Match(string(pair.Tests.Suite.Source)) != nil {
		return nil, errRetainedUnsupported
	}
	input := workspace.MappedInput{SuiteName: a.Name, MemberID: a.Name, Description: a.Description, Model: a.Model, Protocol: a.Protocol, Runtime: workspace.RuntimeProfile{ID: "orka"}, Instructions: []byte(a.Instructions), Sources: workspace.SourceBundle{Format: "kmx.orka-agent-markdown", SchemaVersion: 1, Agent: bytes.Clone(a.Source), Tests: bytes.Clone(pair.Tests.Suite.Source)}}
	for _, example := range pair.Tests.Suite.Examples {
		assertions := slices.Clone(example.Assertions)
		for i := range assertions {
			if score := assertions[i].MinScore; score != nil {
				v := *score
				assertions[i].MinScore = &v
			}
		}
		input.Cases = append(input.Cases, runtime.EvaluationCase{ID: example.ID, Input: example.Input, Assertions: assertions})
	}
	s := &retainedFileSnapshot{pair: pair, input: input, binding: retainedFileBinding{Agent: pair.AgentPath, Tests: pair.Tests.Selection.Path, Explicit: pair.Tests.Selection.Explicit, Revision: pair.SnapshotRevision, AgentRevision: a.Revision, TestsRevision: pair.Tests.Revision}}
	if err := s.recheck(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *retainedFileSnapshot) recheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.pair == nil {
		return errRetainedSourceChanged
	}
	tests := ""
	if s.binding.Explicit {
		tests = s.binding.Tests
	}
	current, err := agentfile.LoadOrkaPair(s.binding.Agent, tests)
	if err != nil || !s.pair.SameSourceVersion(current) || current.Tests.Selection.Explicit != s.binding.Explicit || current.Tests.Selection.Path != s.binding.Tests {
		return errRetainedSourceChanged
	}
	return ctx.Err()
}
