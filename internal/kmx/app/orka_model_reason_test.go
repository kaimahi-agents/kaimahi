package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestOrkaModelObservationDiagnosticReason(t *testing.T) {
	for _, tc := range []struct{ damage, reason string }{
		{"identity", "model_resource_identity_invalid"},
		{"ownership", "model_resource_ownership_changed"},
		{"generation-shape", "model_resource_generation_invalid"},
		{"spec", "model_resource_spec_changed"},
		{"generation", "model_resource_generation_changed"},
	} {
		t.Run(tc.damage, func(t *testing.T) {
			want, obj := modelTerminationObservation("Deployment")
			meta := modelMap(obj["metadata"])
			switch tc.damage {
			case "identity":
				obj["kind"] = "foreign"
			case "ownership":
				meta["uid"] = "foreign"
			case "generation-shape":
				meta["generation"] = float64(0)
			case "spec":
				obj["spec"] = map[string]any{"private-payload": "https://private.invalid/Bearer-secret"}
			case "generation":
				meta["generation"] = float64(3)
			}
			_, cause := modelObserved(OrkaNamespace, want, obj)
			if cause == nil {
				t.Fatal("expected observation refusal")
			}
			wrapped := modelFailure("execution_unknown", fmt.Errorf("private-url https://private.invalid/Bearer-secret: %w", cause))
			var model *OrkaModelError
			if !errors.As(wrapped, &model) {
				t.Fatal("lost model error")
			}
			if model.DiagnosticReason() != tc.reason {
				t.Fatalf("reason %q want %q", model.DiagnosticReason(), tc.reason)
			}
			raw, err := json.Marshal(NewMachineError(model.Code(), "gateway").WithReason(model.DiagnosticReason()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw)+wrapped.Error(), "private") || strings.Contains(string(raw), "Bearer-secret") {
				t.Fatalf("reflected private cause: %s %v", raw, wrapped)
			}
			for _, override := range []error{context.Canceled, context.DeadlineExceeded} {
				stopped := modelFailure("execution_unknown", errors.Join(cause, override)).(*OrkaModelError)
				if stopped.DiagnosticReason() != "" {
					t.Fatal("reason survived cancellation/deadline override")
				}
			}
		})
	}
	for _, cause := range []error{errors.New("model_resource_generation_changed"), errors.New("Bearer-secret https://private.invalid")} {
		if got := modelFailure("execution_unknown", cause).(*OrkaModelError).DiagnosticReason(); got != "" {
			t.Fatalf("classified untyped cause: %q", got)
		}
	}
}

func TestMachineErrorObservationReasonIsClosed(t *testing.T) {
	original := NewMachineError("execution_unknown", "gateway")
	originalJSON, _ := json.Marshal(original)
	for _, reason := range []string{"model_resource_identity_invalid", "model_resource_ownership_changed", "model_resource_generation_invalid", "model_resource_spec_changed", "model_resource_generation_changed"} {
		safe := original.WithReason(reason)
		raw, err := json.Marshal(safe)
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]string
		if err = json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row["reason"] != reason || row["code"] != "execution_unknown" || row["stage"] != "gateway" || row["message"] != "The execution outcome is unknown." || row["recovery"] != "inspect_execution" || safe.Error() != original.Error() {
			t.Fatalf("reason altered safe contract: %s", raw)
		}
		after, _ := json.Marshal(original)
		if string(after) != string(originalJSON) {
			t.Fatal("WithReason mutated original")
		}
	}
	for _, reason := range []string{"", "private-url https://private.invalid/Bearer-secret", "MODEL_RESOURCE_GENERATION_CHANGED"} {
		raw, _ := json.Marshal(original.WithReason(reason))
		var row map[string]any
		json.Unmarshal(raw, &row)
		if _, ok := row["reason"]; ok || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "Bearer-secret") {
			t.Fatalf("unknown reason escaped: %s", raw)
		}
	}
	for _, code := range []string{"cancelled", "deadline_exceeded", "output_failed", "private-code"} {
		raw, _ := json.Marshal(NewMachineError(code, "gateway").WithReason("model_resource_generation_changed"))
		var row map[string]any
		json.Unmarshal(raw, &row)
		if _, ok := row["reason"]; ok {
			t.Fatalf("reason attached to superseding error: %s", raw)
		}
	}
}
