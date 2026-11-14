package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func TestOrkaModelCapabilities(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			deps.goos = goos
			if err := execute([]string{"version", "-ojson"}, deps); err != nil {
				t.Fatal(err)
			}
			records := machineRecords(t, out.Bytes())
			caps := records[0]["capabilities"].([]any)
			found := map[string]bool{}
			for _, value := range caps {
				cap := value.(map[string]any)
				op := cap["operation"].(string)
				if strings.HasPrefix(op, "cluster.orka.model.") {
					found[op] = true
					if cap["output"] != "jsonl" || cap["schemaVersion"] != float64(1) {
						t.Fatal(cap)
					}
				}
			}
			want := 0
			if goos == "linux" {
				want = 2
			}
			if len(found) != want || goos == "linux" && (!found["cluster.orka.model.connect"] || !found["cluster.orka.model.disconnect"]) {
				t.Fatalf("unsupported or missing model capabilities: %v", found)
			}
		})
	}
}

func TestOrkaModelWindowsAndFlags(t *testing.T) {
	for _, verb := range []string{"connect", "disconnect"} {
		args := []string{"cluster", "orka", "model", verb, "--namespace", "orka-system", "--provider", "copilot", "-ojsonl"}
		if verb == "connect" {
			args = append(args, "--route", "copilot", "--model", "gpt-4.1", "--credential-generation", "22222222-2222-4222-8222-222222222222")
		} else {
			args = append(args, "--connection", "11111111-1111-4111-8111-111111111111")
		}
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.goos = "windows"
		err := execute(args, deps)
		if err == nil || *loads != 0 {
			t.Fatal("Windows reached effects", err, *loads)
		}
		r := setupResultRecord(t, out.Bytes(), 1)
		if r["error"].(map[string]any)["code"] != "unsupported" {
			t.Fatal(r)
		}
	}
	for _, flag := range []string{"--credential-generation", "--token", "--credential-file", "--install", "--route"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute([]string{"cluster", "orka", "model", "disconnect", "-ojsonl", flag, "private-canary"}, deps)
		if err == nil || *loads != 0 || strings.Contains(out.String()+diag.String()+err.Error(), "private-canary") {
			t.Fatal("unsafe flag handling", flag, err)
		}
		setupResultRecord(t, out.Bytes(), 1)
	}
}

// Adapter-only phase fixtures; actual lifecycle cancellation is tested in App.
func TestOrkaModelJSONL(t *testing.T) {
	for _, failure := range []string{"", "preflight", "model", "cancel", "credential_rejected", "provider_unreachable", "model_unavailable"} {
		t.Run(failure, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			m := newProvisioningOutput(&out, "cluster.orka.model.connect", context.Background())
			defer m.stop()
			state := &commandState{deps: deps, machine: m}
			cmd := &cobra.Command{Use: "fixture", SilenceUsage: true, SilenceErrors: true}
			var result app.OrkaModelResult
			bindProvisioningOutput(cmd, state, m.operation, func() provisioningSelection {
				return provisioningSelection{mode: "apply", stages: []string{"gateway", "provider", "model"}, deferPreflight: true, model: &result}
			}, func(a *app.App) error {
				if m.setup.prepared {
					t.Fatal("preflight completed before actual handler checks")
				}
				if failure == "preflight" {
					return errors.New("private-preflight")
				}
				m.observe("Model preflight", "done", nil)
				result = app.OrkaModelResult{Connection: &app.OrkaModelConnection{ID: "11111111-1111-4111-8111-111111111111", State: "unknown"}, RemoteState: "unknown"}
				m.observe("Model model", "active", nil)
				if failure == "cancel" {
					m.cancel()
					return context.Canceled
				}
				if failure == "model" {
					return app.NewMachineError("credential_unavailable", "model")
				}
				if strings.HasPrefix(failure, "credential_") || failure == "provider_unreachable" || failure == "model_unavailable" {
					return &machineCommandError{code: failure, safe: app.NewMachineError(failure, "model")}
				}
				m.observe("Model model", "done", nil)
				result.Connection.State = "model_ready"
				result.RemoteState = "may_continue"
				return nil
			})
			cmd.SetContext(m.ctx)
			cmd.SetOut(&out)
			cmd.SetErr(&diag)
			cmd.SetArgs([]string{"-ojsonl"})
			err := cmd.Execute()
			err = m.finishSetup(err)
			exit := 1
			if failure == "" {
				exit = 0
			}
			if (err == nil) != (exit == 0) || err != nil && exitCode(err) != exit {
				t.Fatal(err)
			}
			terminal := setupResultRecord(t, out.Bytes(), exit)
			if failure == "credential_rejected" || failure == "provider_unreachable" || failure == "model_unavailable" {
				if terminal["error"].(map[string]any)["code"] != failure {
					t.Fatal("lost model error code", terminal)
				}
			}
			pairs := 0
			for _, r := range machineRecords(t, out.Bytes()) {
				if r["stage"] == "preflight" {
					pairs++
					if failure == "preflight" && r["event"] == "stage.finished" && r["status"] == "complete" {
						t.Fatal("premature preflight success")
					}
				}
			}
			if pairs != 2 {
				t.Fatalf("preflight pairs%d", pairs)
			}
			for _, value := range terminal["effects"].([]any) {
				if value.(map[string]any)["mutation"] != "none" {
					t.Fatal("health became install mutation", value)
				}
			}
			if failure != "preflight" && terminal["connection"] == nil {
				t.Fatal("partial result lost ID")
			}
			if terminal["proof"] != nil || terminal["verdict"] != "not_applicable" || strings.Contains(out.String()+diag.String(), "private") {
				t.Fatal(terminal)
			}
		})
	}
}

type modelBreakWriter struct {
	out   bytes.Buffer
	calls int
	limit int
}

func (w *modelBreakWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.limit {
		return len(p) - 1, nil
	}
	return w.out.Write(p)
}
func TestOrkaModelJSONLOutputBreak(t *testing.T) {
	for _, limit := range []int{0, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			w := &modelBreakWriter{limit: limit}
			m := newProvisioningOutput(w, "cluster.orka.model.connect", context.Background())
			defer m.stop()
			var diag bytes.Buffer
			deps, _ := testDependencies(&w.out, &diag)
			state := &commandState{deps: deps, machine: m}
			cmd := &cobra.Command{Use: "fixture", SilenceUsage: true, SilenceErrors: true}
			effects := 0
			bindProvisioningOutput(cmd, state, m.operation, func() provisioningSelection { return provisioningSelection{mode: "apply", deferPreflight: true} }, func(a *app.App) error {
				m.observe("Model preflight", "done", nil)
				m.observe("Model gateway", "active", nil)
				if a.Run.Context.Err() != nil {
					return a.Run.Context.Err()
				}
				effects++
				return nil
			})
			cmd.SetContext(m.ctx)
			cmd.SetOut(&w.out)
			cmd.SetErr(&diag)
			cmd.SetArgs([]string{"-ojsonl"})
			err := cmd.Execute()
			if err = m.finishSetup(err); err == nil || effects != 0 || w.calls != limit+1 {
				t.Fatalf("broken output retried or allowed effects: %v effects%d writes%d", err, effects, w.calls)
			}
		})
	}
}
