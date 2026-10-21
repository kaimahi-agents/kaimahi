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
		err := execute([]string{"model", "list"}, deps)
		if (err != nil) != (status == modelaccess.Failure) {
			t.Fatalf("state=%s err=%v", status, err)
		}
		for _, want := range []string{string(status), "runtime inventory unavailable", "https://ollama.com/download", "requires your action"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q: %s", want, out.String())
			}
		}
	}
}

func TestModelListFailureExitPreservesOtherProviderReports(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		for _, tc := range []struct {
			name    string
			state   modelaccess.State
			reason  string
			wantErr bool
		}{
			{"timeout", modelaccess.Failure, "runtime probe timed out", true},
			{"malformed", modelaccess.Failure, "runtime inventory is malformed", true},
			{"cancelled", modelaccess.Failure, "runtime probe was cancelled", true},
			{"absent", modelaccess.Absent, "runtime is not installed", false},
			{"empty", modelaccess.Empty, "runtime has no models", false},
		} {
			for _, order := range []string{"checked-first", "checked-last"} {
				t.Run(format+"/"+tc.name+"/"+order, func(t *testing.T) {
					var out, diag bytes.Buffer
					reports := []modelaccess.Report{
						{Provider: "foundry-local", State: tc.state, Reason: tc.reason, Models: []modelaccess.Model{}},
						{Provider: "ollama", State: modelaccess.Installed, Models: []modelaccess.Model{{Route: modelaccess.Route{Provider: "ollama", Model: "llama3:8b", Endpoint: "http://localhost:11434/v1", Auth: "none"}}}},
					}
					if order == "checked-last" {
						reports[0], reports[1] = reports[1], reports[0]
					}
					deps := productionDependencies()
					deps.stdout, deps.stderr = &out, &diag
					deps.loadConfig, deps.newApp = nil, nil
					deps.discoverModels = func(context.Context) []modelaccess.Report { return reports }
					err := execute([]string{"model", "list", "-o", format}, deps)
					if (err != nil) != tc.wantErr {
						t.Fatalf("want failure=%t, got err=%v; stdout=%s", tc.wantErr, err, &out)
					}
					if err != nil && strings.Contains(err.Error(), tc.reason) {
						t.Fatalf("error should summarize failure, not copy provider diagnostics: %v", err)
					}
					if format == "json" {
						var got []modelaccess.Report
						if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 2 {
							t.Fatalf("incomplete JSON report: %s; %v", &out, err)
						}
						for i, r := range got {
							if r.Provider != reports[i].Provider || r.State != reports[i].State || r.Reason != reports[i].Reason {
								t.Fatalf("lost provider report: %+v", r)
							}
							if r.Provider == "ollama" && (len(r.Models) != 1 || r.Models[0].Route.Model != "llama3:8b" || r.Models[0].Route.Endpoint != "http://localhost:11434/v1") {
								t.Fatalf("lost installed model route: %+v", r)
							}
						}
					} else {
						for _, want := range []string{"foundry-local", string(tc.state), tc.reason, "ollama", "installed", "llama3:8b", "http://localhost:11434/v1"} {
							if !strings.Contains(out.String(), want) {
								t.Fatalf("report missing %q: %s", want, &out)
							}
						}
					}
				})
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
