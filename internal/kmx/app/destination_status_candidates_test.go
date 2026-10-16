package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDestinationStatusOrphanCandidateRootsAreSeparateFromDeployedAgents(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		a, responses, out, _, _ := statusTestFixture(t)
		responses[statusTestAPI] = `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"core.orka.ai/v1alpha1","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]},{"name":"providers","kind":"Provider","namespaced":true,"verbs":["get","list"]},{"name":"tasks","kind":"Task","namespaced":true,"verbs":["get","list"]}]}`
		provider := statusTestObject("Provider", "orphan-provider")
		task := statusTestObject("Task", "orphan-task")
		for _, doc := range []map[string]any{provider, task} {
			statusTestOwned(doc)
			run := strings.Repeat("ab", 16)
			if malformed {
				run = "malformed"
			}
			doc["metadata"].(map[string]any)["annotations"].(map[string]any)[suiteCandidateRunMarker] = run
			delete(doc, "status")
		}
		responses[statusTestAPI+"/namespaces/"+statusTestNS+"/providers?limit=128"] = statusTestWorkloadList("core.orka.ai/v1alpha1", "Provider", provider)
		responses[statusTestAPI+"/namespaces/"+statusTestNS+"/tasks?limit=128"] = statusTestWorkloadList("core.orka.ai/v1alpha1", "Task", task)
		report, err := statusTestRun(t, a, out)
		if len(report.Agents) != 0 || len(report.Candidates) != 2 {
			t.Fatalf("orphan candidates hidden or deployed: %+v %v", report, err)
		}
		if malformed {
			if err == nil || report.Complete {
				t.Fatal("malformed candidate ownership became complete")
			}
		} else if err != nil || !report.Complete {
			t.Fatalf("candidate progress changed deployed readiness: %+v %v", report, err)
		}
		raw, _ := json.Marshal(report)
		if !strings.Contains(string(raw), `"kind":"Provider"`) || !strings.Contains(string(raw), `"kind":"Task"`) {
			t.Fatalf("candidate kinds lost: %s", raw)
		}
	}
}

func TestDestinationStatusPendingCandidateDoesNotChangeDeploymentExit(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "candidate")
	statusTestOwned(agent)
	agent["metadata"].(map[string]any)["annotations"].(map[string]any)[suiteCandidateRunMarker] = strings.Repeat("ab", 16)
	delete(agent, "status")
	statusTestAddAgent(t, responses, agent)
	report, err := statusTestRun(t, a, out)
	if err != nil || !report.Complete || len(report.Agents) != 0 || len(report.Candidates) != 1 {
		t.Fatalf("candidate evaluation confused with deployment readiness: %+v %v", report, err)
	}
}
