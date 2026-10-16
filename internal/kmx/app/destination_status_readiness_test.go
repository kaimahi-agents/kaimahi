package app

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDestinationStatusOrkaReadinessAndExitPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		complete    bool
		mutate      func(map[string]any)
	}{
		{"ready", "ready", true, func(map[string]any) {}},
		{"stale", "pending", true, func(d map[string]any) { d["metadata"].(map[string]any)["generation"] = 2 }},
		{"no-status-yet", "pending", true, func(d map[string]any) { delete(d, "status") }},
		{"rejected", "unhealthy", true, func(d map[string]any) {
			d["status"].(map[string]any)["ready"] = false
			d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "False"
		}},
		{"unknown", "unknown", false, func(d map[string]any) {
			d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "Unknown"
		}},
		{"contradiction", "unknown", false, func(d map[string]any) { d["status"].(map[string]any)["ready"] = false }},
		{"malformed-status", "unknown", false, func(d map[string]any) { d["status"] = "private-condition-payload" }},
		{"duplicate-condition", "unknown", false, func(d map[string]any) {
			d["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}, map[string]any{"type": "Ready", "status": "False", "observedGeneration": 1}}
		}},
		{"future-condition", "unknown", false, func(d map[string]any) {
			d["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = 2
		}},
		{"case-folded-ready", "pending", true, func(d map[string]any) { s := d["status"].(map[string]any); delete(s, "ready"); s["Ready"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent := statusTestObject("Agent", "greeter")
			tc.mutate(agent)
			statusTestAddAgent(t, responses, agent)
			report, err := statusTestRun(t, a, out)
			if len(report.Agents) != 1 || report.Agents[0].Readiness.State != tc.state || report.Complete != tc.complete {
				t.Fatalf("wrong readiness state: %+v %v", report, err)
			}
			if (tc.state == "pending") != errors.Is(err, ErrDestinationStatusPending) || tc.state != "ready" && err == nil {
				t.Fatalf("wrong exit category: %s %v", tc.state, err)
			}
		})
	}
}

func TestDestinationStatusPrerequisiteUnknownRetainsAgentProvenance(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	statusTestStamp(t, agent)
	statusTestAddAgent(t, responses, agent)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/providers/greeter"] = "ERROR:Forbidden:private-provider-content"
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Provenance.State != "recorded" || report.Agents[0].Readiness.State != "unknown" || report.Agents[0].Resources[1].Provenance.State != "unknown" {
		t.Fatalf("partial observation lost: %+v %v", report, err)
	}
}

func TestDestinationStatusObservedSpecDriftIsNotReady(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	statusTestStamp(t, agent)
	agent["spec"].(map[string]any)["systemPrompt"] = "private-authored-payload"
	statusTestAddAgent(t, responses, agent)
	report, err := statusTestRun(t, a, out)
	if err == nil || errors.Is(err, ErrDestinationStatusPending) || !report.Complete || report.Agents[0].Readiness.State != "drifted" || report.Agents[0].Resources[0].SpecDrift != "drifted" || report.Agents[0].Provenance.State != "recorded" {
		t.Fatalf("spec drift accepted or provenance erased: %+v %v", report, err)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "private-authored-payload") {
		t.Fatal("spec payload printed")
	}
}

func TestDestinationStatusFailureTakesPrecedenceOverPending(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	pending := statusTestObject("Agent", "pending")
	delete(pending, "status")
	statusTestAddAgent(t, responses, pending)
	unknown := statusTestObject("Agent", "unknown")
	unknown["status"] = "malformed"
	statusTestAddAgent(t, responses, unknown)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = statusTestList(pending, unknown)
	report, err := statusTestRun(t, a, out)
	if len(report.Agents) != 2 || err == nil || errors.Is(err, ErrDestinationStatusPending) || report.Complete {
		t.Fatalf("pending hid unknown: %+v %v", report, err)
	}
}

func TestDestinationStatusTableAndJSONShareProvenanceAndCompanionFacts(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	statusTestStamp(t, agent)
	statusTestAddAgent(t, responses, agent)
	report, err := statusTestRun(t, a, out)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{report.Agents[0].Name, report.Agents[0].Member, "sha256:" + strings.Repeat("1", 12), "sha256:" + strings.Repeat("4", 12), "dev", "promoted", "not evaluated on this target", "2026-10-16T12:20:00Z", "not recorded", "Provider/greeter", "provenance not recorded", "ready"} {
		if !strings.Contains(out.String(), value) {
			t.Errorf("table lost JSON fact %q: %s", value, out)
		}
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatal("styled report")
	}
}

type statusBrokenWriter struct{}

func (statusBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestDestinationStatusOutputFailuresAreNotSuccess(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		a, _, _, _, _ := statusTestFixture(t)
		a.Out = statusBrokenWriter{}
		if err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: format}); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("output error lost: %v", err)
		}
	}
}
