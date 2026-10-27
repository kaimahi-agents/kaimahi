package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestTestNewAppDefaultsCanonicalRecovery(t *testing.T) {
	a := New(&config.Config{})
	if got := a.withRunContext(t.Context()).testCommandVerb(); got != "package test" {
		t.Fatalf("normal App and worker recovery uses %q, want test", got)
	}
}

func TestTestCandidateRecoveryPreservesLegacySpelling(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		name := "eval"
		if canonical {
			name = "package test"
		}
		t.Run(name, func(t *testing.T) {
			a, _, f, _ := publicNativeEvaluationFixture(t)
			a.TestPresentation = canonical
			a.Executable = "./bin with space/eval"
			diagnostics := &bytes.Buffer{}
			a.Err = diagnostics
			opt := nativeEvaluationOptions(t)
			opt.ResultPort = f.opt.ResultPort
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for _, frame := range p.inventory {
				f.answers[frame.Case.Input] = nativeCanary + " answer"
			}
			if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(diagnostics.String(), "Recovery: './bin with space/eval' "+name+" cleanup") {
				t.Fatalf("wrong recovery verb or opaque executable changed: %s", diagnostics)
			}
		})
	}
}

func TestTestTargetsTableKeepsMachineOperations(t *testing.T) {
	var table, wire bytes.Buffer
	a := &App{Out: &table}
	if err := a.Targets(TargetsOptions{Output: "table"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "test: supported") || strings.Contains(table.String(), "  eval:") || strings.Contains(table.String(), "create/eval") {
		t.Fatalf("table does not teach test: %s", &table)
	}
	a.Out = &wire
	if err := a.Targets(TargetsOptions{Output: "json"}); err != nil {
		t.Fatal(err)
	}
	var report TargetsReport
	if err := json.Unmarshal(wire.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wire.String(), `"operation":"eval"`) || strings.Contains(wire.String(), `"operation":"test"`) || !strings.Contains(wire.String(), "create/eval") {
		t.Fatalf("machine operations changed: %s", &wire)
	}
}
