package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMachineErrorVocabulary(t *testing.T) {
	// A raw cause or a fallback for every code would lose safe recovery semantics.
	for _, tc := range []struct{ code, message, recovery string }{
		{"invalid_arguments", "The command arguments are invalid.", "check_inputs"},
		{"configuration_invalid", "The configuration is invalid.", "check_configuration"},
		{"unsupported", "The requested operation is unsupported.", "check_inputs"},
		{"confirmation_required", "Target confirmation is required.", "confirm_target"},
		{"credential_unavailable", "A required credential is unavailable.", "supply_credentials"},
		{"permission_denied", "The operation was not permitted.", "inspect_target"},
		{"dependency_unavailable", "A required dependency is unavailable.", "check_configuration"},
		{"operation_failed", "The operation failed.", "inspect_target"},
		{"execution_unknown", "The execution outcome is unknown.", "inspect_execution"},
		{"cancelled", "The operation was cancelled.", "inspect_execution"},
		{"deadline_exceeded", "The operation deadline was exceeded.", "inspect_execution"},
		{"output_failed", "Cannot write machine output.", "none"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := NewMachineError(tc.code, "preflight")
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 4 || got["code"] != tc.code || got["message"] != tc.message || got["recovery"] != tc.recovery || got["stage"] != "preflight" || e.Error() != tc.message {
				t.Fatalf("unsafe or incorrect error: %s (%v)", b, e)
			}
			if len(got["message"]) > 256 {
				t.Fatal("unbounded message")
			}
		})
	}
}

func TestMachineErrorWithholdsUnknownValues(t *testing.T) {
	for _, e := range []*MachineError{NewMachineError("private-provider-body", "private-url"), {}} {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got["code"] != "operation_failed" || got["message"] != "The operation failed." || strings.Contains(string(b), "private") {
			t.Fatalf("untrusted values escaped: %s", b)
		}
	}
	for _, stage := range []string{"cluster", "ollama", "model", "orka", "installer", "runtime", "result_reader", "provider", "execution", "assertions", "finalize", "other"} {
		b, _ := json.Marshal(NewMachineError("operation_failed", stage))
		var got map[string]string
		if err := json.Unmarshal(b, &got); err != nil || got["stage"] != stage {
			t.Fatalf("%s: %s", stage, b)
		}
	}
}
