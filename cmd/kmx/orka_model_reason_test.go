package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

// Typed observation classification is a trusted internal error-chain contract;
// the wire projection must still reject any value outside its fixed vocabulary.
type modelReasonFixtureError struct {
	reason string
	cause  error
}

func (e *modelReasonFixtureError) Error() string {
	return "Bearer-secret https://private.invalid/provider-body"
}
func (e *modelReasonFixtureError) Unwrap() error            { return e.cause }
func (e *modelReasonFixtureError) DiagnosticReason() string { return e.reason }

func TestOrkaModelJSONLObservationReason(t *testing.T) {
	for _, damage := range []string{"", "unknown", "cancel", "deadline", "other-operation"} {
		t.Run(damage, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			operation := "cluster.orka.model.disconnect"
			if damage == "other-operation" {
				operation = "cluster.up"
			}
			m := newProvisioningOutput(&out, operation, context.Background())
			defer m.stop()
			state := &commandState{deps: deps, machine: m}
			cmd := &cobra.Command{Use: "fixture", SilenceUsage: true, SilenceErrors: true}
			result := app.OrkaModelResult{Connection: &app.OrkaModelConnection{ID: "11111111-1111-4111-8111-111111111111", State: "unknown"}, RemoteState: "unknown"}
			bindProvisioningOutput(cmd, state, operation, func() provisioningSelection {
				if damage == "other-operation" {
					return upSelection(app.UpOptions{})
				}
				return provisioningSelection{mode: "apply", stages: []string{"gateway", "provider", "finalize"}, deferPreflight: true, model: &result}
			}, func(a *app.App) error {
				if damage == "other-operation" {
					m.observe("Prepare kind cluster", "active", nil)
				} else {
					m.observe("Model preflight", "done", nil)
					m.observe("Model gateway", "active", nil)
				}
				reason := "model_resource_generation_changed"
				var cause error
				switch damage {
				case "unknown":
					reason = "Bearer-secret https://private.invalid/provider-body"
				case "cancel":
					m.cancel()
					cause = context.Canceled
				case "deadline":
					cause = context.DeadlineExceeded
				}
				return &machineCommandError{code: "execution_unknown", cause: &modelReasonFixtureError{reason: reason, cause: cause}, safe: app.NewMachineError("execution_unknown", "gateway")}
			})
			cmd.SetContext(m.ctx)
			cmd.SetOut(&out)
			cmd.SetErr(&diag)
			cmd.SetArgs([]string{"-ojsonl"})
			err := m.finishSetup(cmd.Execute())
			if err == nil || exitCode(err) != 1 {
				t.Fatal("failure exit lost", err)
			}
			terminal := setupResultRecord(t, out.Bytes(), 1)
			failure := terminal["error"].(map[string]any)
			wantCode := "execution_unknown"
			if damage == "cancel" {
				wantCode = "cancelled"
			}
			if damage == "deadline" {
				wantCode = "deadline_exceeded"
			}
			if failure["code"] != wantCode {
				t.Fatalf("override lost: %v", terminal)
			}
			if damage == "" {
				if failure["reason"] != "model_resource_generation_changed" || failure["stage"] != "gateway" || failure["message"] != "The execution outcome is unknown." {
					t.Fatalf("lost safe inner reason: %v", failure)
				}
			} else if _, ok := failure["reason"]; ok {
				t.Fatalf("untrusted or superseded reason escaped: %v", failure)
			}
			if damage != "other-operation" && terminal["connection"] == nil {
				t.Fatal("partial connection lost")
			}
			if strings.Contains(out.String()+diag.String()+err.Error(), "private.invalid") || strings.Contains(out.String()+diag.String()+err.Error(), "Bearer-secret") {
				t.Fatal("raw cause escaped")
			}
		})
	}
}

func TestOrkaModelReasonDoesNotSurviveBrokenWriter(t *testing.T) {
	writer := &machineFaultWriter{short: true}
	m := newProvisioningOutput(writer, "cluster.orka.model.disconnect", context.Background())
	defer m.stop()
	m.selectSetup(provisioningSelection{mode: "apply", deferPreflight: true})
	if m.beginPreflight() == nil {
		t.Fatal("expected output failure")
	}
	err := m.finishSetup(&machineCommandError{code: "execution_unknown", cause: &modelReasonFixtureError{reason: "model_resource_generation_changed"}, safe: app.NewMachineError("execution_unknown", "gateway")})
	var reported *machineCommandError
	if !errors.As(err, &reported) || reported.safe == nil {
		t.Fatal("lost safe output error", err)
	}
	raw, _ := json.Marshal(reported.safe)
	var failure map[string]any
	json.Unmarshal(raw, &failure)
	if failure["code"] != "output_failed" {
		t.Fatalf("writer did not override: %s", raw)
	}
	if _, ok := failure["reason"]; ok || writer.calls != 1 {
		t.Fatalf("reason survived broken output or output retried: %s calls%d", raw, writer.calls)
	}
}
