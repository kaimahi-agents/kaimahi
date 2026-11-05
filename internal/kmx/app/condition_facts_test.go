package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestConditionPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, status, placement, discovery string
		valid                              bool
	}{
		{"missing-status", `null`, "pending", "Unknown", true},
		{"bad-status", `"bad"`, "unknown", "", false},
		{"no-conditions", `{"ready":true}`, "pending", "Unknown", true},
		{"null-conditions", `{"ready":true,"conditions":null}`, "pending", "Unknown", true},
		{"bad-conditions", `{"ready":true,"conditions":{}}`, "unknown", "", false},
		{"null-row", `{"ready":true,"conditions":[null]}`, "unknown", "", false},
		{"empty-type", `{"ready":true,"conditions":[{"type":"","status":"True"}]}`, "unknown", "", false},
		{"bad-state", `{"ready":true,"conditions":[{"type":"Ready","status":"true"}]}`, "unknown", "", false},
		{"ready", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}`, "ready", "True", true},
		{"not-ready", `{"ready":false,"conditions":[{"type":"Ready","status":"False","observedGeneration":1}]}`, "unhealthy", "False", true},
		{"unknown", `{"ready":true,"conditions":[{"type":"Ready","status":"Unknown","observedGeneration":1}]}`, "unknown", "Unknown", true},
		{"contradiction", `{"ready":false,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}`, "unknown", "Unknown", true},
		{"opposite-contradiction", `{"ready":true,"conditions":[{"type":"Ready","status":"False","observedGeneration":1}]}`, "unknown", "Unknown", true},
		{"no-bool", `{"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}`, "pending", "Unknown", true},
		{"null-bool", `{"ready":null,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}`, "pending", "", false},
		{"bad-bool", `{"ready":"true","conditions":[]}`, "unknown", "", false},
		{"omitted-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True"}]}`, "pending", "Unknown", true},
		{"null-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":null}]}`, "unknown", "", false},
		{"future-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":2}]}`, "unknown", "Unknown", true},
		{"fractional-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":0.5}]}`, "unknown", "", false},
		{"negative-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":-1}]}`, "unknown", "", false},
		{"oversized-generation", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":9007199254740992}]}`, "unknown", "", false},
		{"duplicate", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1},{"type":"Ready","status":"False","observedGeneration":1}]}`, "unknown", "", false},
		{"case-distinct", `{"ready":true,"conditions":[{"type":"ready","status":"False","observedGeneration":1},{"type":"Ready","status":"True","observedGeneration":1}]}`, "ready", "True", true},
		{"case-folded", `{"Ready":true,"conditions":[{"Type":"Ready","status":"True","observedGeneration":1}]}`, "unknown", "", false},
		{"message-and-bad-reason", `{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1,"reason":{},"message":"controller-payload-canary"}]}`, "ready", "True", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(`{"metadata":{"generation":1},"status":`+tc.status+`}`), &doc); err != nil {
				t.Fatal(err)
			}
			got := statusPrimaryReadiness("orka", doc)
			reasons := map[string]string{
				"pending":   "current-generation readiness has not been acknowledged",
				"unknown":   "primary identity or readiness is malformed or indeterminate",
				"ready":     "current-generation Ready placement",
				"unhealthy": "current-generation primary reports not Ready",
			}
			state, valid := discoveryProviderCondition(doc, 1)
			if got.State != tc.placement || got.Reason != reasons[tc.placement] || state != tc.discovery || valid != tc.valid {
				t.Fatalf("placement=%+v discovery=%q/%t", got, state, valid)
			}
			if strings.Contains(got.Reason+state, "controller-payload-canary") {
				t.Fatal("raw controller message escaped")
			}
		})
	}
}

func TestConditionPoliciesRejectOversizeBeforeDecoding(t *testing.T) {
	doc := map[string]any{
		"metadata": map[string]any{"generation": float64(1)},
		"status":   map[string]any{"conditions": make([]any, 65)},
	}
	var got destinationStatusReadiness
	allocations := testing.AllocsPerRun(3, func() { got = statusPrimaryReadiness("orka", doc) })
	if got.State != "unknown" || allocations != 0 {
		t.Fatalf("oversized conditions allocated row facts: state=%s allocations=%g", got.State, allocations)
	}
}

func TestConditionPoliciesKeepCallerLimits(t *testing.T) {
	for _, count := range []int{64, 65} {
		rows := []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": float64(1)}}
		for i := 1; i < count; i++ {
			rows = append(rows, map[string]any{"type": fmt.Sprint(i), "status": "True"})
		}
		doc := map[string]any{"metadata": map[string]any{"generation": float64(1)}, "status": map[string]any{"ready": true, "conditions": rows}}
		want := "ready"
		if count == 65 {
			want = "unknown"
		}
		if got := statusPrimaryReadiness("orka", doc); got.State != want {
			t.Fatal(count, got)
		}
		if state, valid := discoveryProviderCondition(doc, 1); state != "True" || !valid {
			t.Fatal(count, state, valid)
		}
	}
}
