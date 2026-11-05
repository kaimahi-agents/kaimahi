package workspace

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func mappedFixture() MappedInput {
	runs, minimum, score := 2, 1, 80
	return MappedInput{SuiteName: "greeter", MemberID: "greeter", Model: "qwen-3.5-2b", Protocol: "openai-compatible", Runtime: RuntimeProfile{ID: "orka"}, Instructions: []byte("Answer briefly.\n"),
		Sources: SourceBundle{Format: "kmx.orka-agent-markdown", SchemaVersion: 1, Agent: []byte("# Original agent\r\n"), Tests: []byte("# Original tests\r\n")},
		Cases:   []runtime.EvaluationCase{{ID: "greeting", Input: "Ada", ExpectContains: []string{"Ada"}, Runs: &runs, MinPasses: &minimum, Assertions: []runtime.EvaluationAssertion{{ID: "judge", Type: "llmJudge", Rubric: "Say hello", MinScore: &score, JudgeModel: "qwen-3.5-2b", JudgeVersion: "v1"}}}},
	}
}

func TestMappedFreezeOwnsInputs(t *testing.T) {
	input := mappedFixture()
	frozen, err := freezeMapped(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Sources.Agent[0] = 'X'
	input.Sources.Tests[0] = 'X'
	input.Instructions[0] = 'X'
	input.Cases[0].ExpectContains[0] = "wrong"
	input.Cases[0].Assertions[0].Rubric = "wrong"
	*input.Cases[0].Runs = 99
	*input.Cases[0].MinPasses = 99
	*input.Cases[0].Assertions[0].MinScore = 1
	if !reflect.DeepEqual(frozen.value, mappedFixture()) {
		t.Fatal("caller mutation changed frozen input")
	}
}

func TestMappedRefusesUnsupportedBeforeStaging(t *testing.T) {
	for name, change := range map[string]func(*MappedInput){
		"runtime":      func(i *MappedInput) { i.Runtime = RuntimeProfile{ID: "kagent"} },
		"zero runtime": func(i *MappedInput) { i.Runtime = RuntimeProfile{} },
		"protocol":     func(i *MappedInput) { i.Protocol = "other" },
		"model":        func(i *MappedInput) { i.Model = "" },
		"empty cases":  func(i *MappedInput) { i.Cases = nil },
		"129 cases":    func(i *MappedInput) { i.Cases = make([]runtime.EvaluationCase, 129) },
		"collision":    func(i *MappedInput) { c := i.Cases[0]; c.ID = "GREETING"; i.Cases = append(i.Cases, c) },
		"tool": func(i *MappedInput) {
			i.Cases[0].Assertions = []runtime.EvaluationAssertion{{ID: "tool", Type: "toolCalled", Tool: "fetch"}}
		},
		"judge":             func(i *MappedInput) { i.Cases[0].Assertions[0].JudgeVersion = "" },
		"feature":           func(i *MappedInput) { i.RequiredFeatures = []string{"sentinel-author-value"} },
		"unknown":           func(i *MappedInput) { i.UnsupportedFields = []string{"sentinel-author-value"} },
		"source size":       func(i *MappedInput) { i.Sources.Agent = bytes.Repeat([]byte("x"), (256<<10)+1) },
		"source format":     func(i *MappedInput) { i.Sources.Format = "other" },
		"missing companion": func(i *MappedInput) { i.Sources.Tests = nil },
	} {
		t.Run(name, func(t *testing.T) {
			input := mappedFixture()
			change(&input)
			if _, err := freezeMapped(input); err == nil || strings.Contains(err.Error(), "sentinel-author-value") {
				t.Fatalf("expected safe refusal, got %v", err)
			}
		})
	}
}

func TestMappedBaseMatchesWorkspaceGraph(t *testing.T) {
	input := mappedFixture()
	input.Cases = nil
	for n := 0; n < 128; n++ {
		input.Cases = append(input.Cases, runtime.EvaluationCase{ID: fmt.Sprintf("case-%03d", n), Input: "Ada", ExpectContains: []string{"Ada"}})
	}
	frozen, err := freezeMapped(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveMappedBase(context.Background(), frozen)
	if err != nil {
		t.Fatal(err)
	}
	member := sourceMember{ID: "greeter", Runtime: &sourceRuntime{ID: "orka"}}
	member.Model.Protocol = input.Protocol
	member.Model.Model = input.Model
	want, err := resolve(input.SuiteName, member, input.Instructions, defaultProfile, input.Cases)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("mapped input changed no-policy workspace graph")
	}
	for n, c := range input.Cases {
		parsed, err := DecodePackagedCase(got[evalCasePath("greeter", c.ID)])
		if err != nil || parsed.ID != c.ID || parsed.ExpectContains[0] != "Ada" {
			t.Fatalf("case %d changed: %+v %v", n, parsed, err)
		}
	}
}
