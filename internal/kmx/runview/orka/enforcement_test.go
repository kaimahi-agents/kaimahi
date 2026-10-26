package orka

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnforcementObservationIsCurrentTypedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name, status, reason, binding string
		generation                    int64
		receipt                       bool
		want                          string
	}{
		{"admitted", "True", "EnforcementVerified", "container:" + strings.Repeat("a", 64), 3, true, "admitted"},
		{"stale", "True", "EnforcementVerified", "container:" + strings.Repeat("a", 64), 2, true, "not confirmed"},
		{"legacy", "True", "EnforcementVerified", "", 3, false, "admission reported, physical receipt unavailable"},
		{"invalid-receipt", "True", "EnforcementVerified", "private-binding-content", 3, true, "admission reported, physical receipt unavailable"},
		{"refused", "False", "EnforcementRefused", "", 3, false, "refused; workload admission not confirmed"},
		{"pending", "False", "EnforcementPending", "", 3, false, "waiting for enforcement; workload admission not confirmed"},
		{"communication", "Unknown", "EnforcementUncertain", "", 3, false, "uncertain; workload admission not confirmed"},
		{"unrecognized", "True", "ObserveOnly", "", 3, false, "not confirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := map[string]any{
				"conditions": []any{map[string]any{
					"type": "EnforcementReady", "status": test.status, "reason": test.reason,
					"observedGeneration": test.generation, "message": "private-status-content",
				}},
			}
			if test.receipt {
				status["enforcementAdmission"] = map[string]any{
					"bindingID": test.binding, "requirementDigest": "sha256:" + strings.Repeat("b", 64),
					"observedGeneration": test.generation,
				}
			}
			raw, err := json.Marshal(map[string]any{
				"metadata": map[string]any{"generation": 3},
				"spec":     map[string]any{"enforcement": map[string]any{"mode": "enforce", "policy": "private-policy-content"}},
				"status":   status,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ObserveEnforcement(raw)
			if err != nil || got == nil || got.State != test.want {
				t.Fatalf("observation = %#v, %v; want %s", got, err, test.want)
			}
			rendered := got.Summary()
			if strings.Contains(rendered, "private-") {
				t.Fatalf("private content escaped projection: %q", rendered)
			}
			if got.State == "admitted" && (!strings.Contains(rendered, test.binding) ||
				!strings.Contains(rendered, "not a live provider check")) {
				t.Fatalf("admission lost physical identity or historical scope: %q", rendered)
			}
		})
	}
}

func TestEnforcementObservationDoesNotInferEnrollment(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"spec":{"enforcement":null}}`,
		`{"status":{"enforcementAdmission":{"bindingID":"container:old"}}}`,
	} {
		if got, err := ObserveEnforcement([]byte(raw)); err != nil || got != nil {
			t.Fatalf("unenrolled Task gained an observation: %#v, %v", got, err)
		}
	}
	if _, err := ObserveEnforcement([]byte(`{"status":`)); err == nil {
		t.Fatal("malformed response became an absent observation")
	}
}

func TestEnforcementObservationRejectsUnsupportedModesDespiteReceipt(t *testing.T) {
	for _, requirement := range []string{`{}`, `{"mode":"observe"}`, `"not an object"`, `[]`} {
		raw := `{"metadata":{"generation":1},"spec":{"enforcement":` + requirement + `},
			"status":{"conditions":[{"type":"EnforcementReady","status":"True","reason":"EnforcementVerified","observedGeneration":1}],
			"enforcementAdmission":{"observedGeneration":1,"bindingID":"container:` + strings.Repeat("a", 64) +
			`","requirementDigest":"sha256:` + strings.Repeat("b", 64) + `"}}}`
		got, err := ObserveEnforcement([]byte(raw))
		if err != nil || got == nil || got.State == "admitted" || got.BindingID != "" || got.RequirementDigest != "" {
			t.Fatalf("unsupported requirement gained an admission observation: %#v, %v", got, err)
		}

	}
}

func TestActionableEnforcementOutcomeIsIndependentAndGenerationPinned(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome any
		valid   bool
	}{
		{"current", map[string]any{"observedGeneration": 3, "reason": "EnforcementRefused", "needs": []string{"supported-format", "complete-policy"}}, true},
		{"malformed", "private-provider-details", false},
		{"stale", map[string]any{"observedGeneration": 2, "reason": "EnforcementRefused", "needs": []string{"supported-format"}}, false},
		{"mismatched", map[string]any{"observedGeneration": 3, "reason": "EnforcementPending", "needs": []string{"supported-format"}}, false},
		{"unknown-need", map[string]any{"observedGeneration": 3, "reason": "EnforcementRefused", "needs": []string{"private-provider-details"}}, false},
		{"malformed-needs", map[string]any{"observedGeneration": 3, "reason": "EnforcementRefused", "needs": "private-provider-details"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"kind":     "Task",
				"metadata": map[string]any{"name": "protected", "namespace": "default", "uid": "task-uid", "generation": 3},
				"spec":     map[string]any{"enforcement": map[string]any{"mode": "enforce", "policy": "private-policy"}},
				"status": map[string]any{
					"phase": "Failed", "message": "private-provider-details",
					"enforcementOutcome":   test.outcome,
					"enforcementAdmission": "private-malformed-optional-receipt",
					"conditions": []any{map[string]any{
						"type": "EnforcementReady", "status": "False", "reason": "EnforcementRefused", "observedGeneration": 3,
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			task, err := decodeTask(raw, "default")
			if err != nil || task.Enforcement == nil || task.Metadata.UID != "task-uid" {
				t.Fatalf("optional diagnostics hid the Task: %#v, %v", task, err)
			}
			message := task.Enforcement.Summary()
			if strings.Contains(message, "private-") || !strings.Contains(message, "refused") {
				t.Fatalf("unsafe or misleading diagnostic: %q", message)
			}
			if test.valid {
				if !strings.Contains(message, "format supported") || !strings.Contains(message, "complete supported policy") {
					t.Fatalf("actionable needs were dropped: %q", message)
				}
			} else if !strings.Contains(message, "unavailable or stale") {
				t.Fatalf("unusable detail was not made visible: %q", message)
			}
		})
	}
}
