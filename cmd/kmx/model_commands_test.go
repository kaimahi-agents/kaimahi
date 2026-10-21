package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestModelListIsConfigFreeAndNonSecret(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			deps := productionDependencies()
			deps.stdout = &out
			deps.stderr = &diag
			deps.loadConfig = nil
			deps.newApp = nil
			deps.discoverModels = func(context.Context) []modelaccess.Report {
				calls++
				return []modelaccess.Report{{Provider: "ollama", State: modelaccess.Empty, Models: []modelaccess.Model{}, Install: modelaccess.InstallGuidance("ollama", "linux", "amd64")}}
			}
			if err := execute([]string{"model", "list", "-o", format}, deps); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(out.String(), "empty") {
				t.Fatalf("calls=%d output=%s", calls, out.String())
			}
			if format == "json" {
				var reports []modelaccess.Report
				if err := json.Unmarshal(out.Bytes(), &reports); err != nil || len(reports) != 1 {
					t.Fatalf("%s %v", out.String(), err)
				}
			}
		})
	}
}
func TestModelListExplainsAbsentAndFailure(t *testing.T) {
	for _, status := range []modelaccess.State{modelaccess.Absent, modelaccess.Failure} {
		var out bytes.Buffer
		deps := productionDependencies()
		deps.stdout = &out
		deps.stderr = &bytes.Buffer{}
		deps.discoverModels = func(context.Context) []modelaccess.Report {
			return []modelaccess.Report{{Provider: "ollama", State: status, Reason: "runtime inventory unavailable", Install: modelaccess.InstallGuidance("ollama", "linux", "amd64")}}
		}
		if err := execute([]string{"model", "list"}, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{string(status), "runtime inventory unavailable", "https://ollama.com/download", "requires your action"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, out.String())
			}
		}
	}
}

func TestModelHelpAndRefusalsNeverProbe(t *testing.T) {
	for _, args := range [][]string{{"model"}, {"model", "--help"}, {"model", "list", "--help"}, {"model", "list", "-o", "bad"}, {"model", "list", "extra"}, {"__complete", "model", ""}} {
		deps := productionDependencies()
		deps.stdout = &bytes.Buffer{}
		deps.stderr = &bytes.Buffer{}
		deps.loadConfig = nil
		deps.newApp = nil
		deps.discoverModels = func(context.Context) []modelaccess.Report { t.Fatal("unexpected probe"); return nil }
		err := execute(args, deps)
		if args[0] == "model" && len(args) > 2 && args[2] != "--help" {
			if err == nil {
				t.Fatalf("accepted invalid arguments: %v", args)
			}
			if args[2] == "-o" && err.Error() != "unsupported output format; use table or json" {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
