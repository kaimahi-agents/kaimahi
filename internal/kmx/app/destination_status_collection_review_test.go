package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDestinationStatusRawWorkloadListItemsUseValidatedEnvelopeType(t *testing.T) {
	for _, path := range []string{"/apis/apps/v1/namespaces/" + statusTestNS + "/replicasets?limit=128&labelSelector=app%3Dgreeter", "/api/v1/namespaces/" + statusTestNS + "/pods?limit=128&labelSelector=app%3Dgreeter"} {
		a, responses, out, _ := statusTestKagentFixture(t)
		var list map[string]any
		if err := json.Unmarshal([]byte(responses[path]), &list); err != nil {
			t.Fatal(err)
		}
		for _, value := range list["items"].([]any) {
			item := value.(map[string]any)
			delete(item, "kind")
			delete(item, "apiVersion")
		}
		statusTestPut(responses, path, list)
		report, err := statusTestRun(t, a, out)
		if err != nil || !report.Complete || len(report.Agents) != 1 || report.Agents[0].Readiness.State != "ready" {
			t.Fatalf("valid typed collection items rejected: %+v %v", report, err)
		}
	}
}

func TestDestinationStatusExplicitListItemTypeConflictsAreNotDefaulted(t *testing.T) {
	for _, field := range []string{"kind", "apiVersion"} {
		for _, bad := range []any{"", nil, "wrong"} {
			a, responses, out, objects := statusTestKagentFixture(t)
			pod := objects["Pod"]
			pod[field] = bad
			responses["/api/v1/namespaces/"+statusTestNS+"/pods?limit=128&labelSelector=app%3Dgreeter"] = statusTestWorkloadList("v1", "Pod", pod)
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete {
				t.Fatalf("explicit %s=%v was replaced: %+v", field, bad, report)
			}
		}
	}
}

func TestDestinationStatusCandidatesRemainVisibleWithoutAgentAPI(t *testing.T) {
	for _, runtime := range []string{"orka", "kagent"} {
		for _, forbidden := range []bool{false, true} {
			a, responses, out, _, _ := statusTestFixture(t)
			prefix, version, kind, plural := statusTestAPI, "core.orka.ai/v1alpha1", "Provider", "providers"
			if runtime == "kagent" {
				prefix, version, kind, plural = statusTestKagentAPI, "kagent.dev/v1alpha2", "ModelConfig", "modelconfigs"
				responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"kagent.dev","versions":[{"groupVersion":"kagent.dev/v1alpha2","version":"v1alpha2"}]}]}`
			}
			statusTestPut(responses, prefix, map[string]any{"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": version, "resources": []any{map[string]any{"name": plural, "kind": kind, "namespaced": true, "verbs": []string{"get", "list"}}}})
			root := statusTestObject(kind, "candidate-root")
			root["apiVersion"] = version
			statusTestOwned(root)
			annotations := root["metadata"].(map[string]any)["annotations"].(map[string]any)
			annotations[suiteCandidateRunMarker] = strings.Repeat("ab", 16)
			if runtime == "kagent" {
				annotations[orkaSuiteMarkers[3]] = "kmx.native-kagent/v2"
			}
			path := prefix + "/namespaces/" + statusTestNS + "/" + plural + "?limit=128"
			responses[path] = statusTestWorkloadList(version, kind, root)
			if forbidden {
				responses[path] = "ERROR:Forbidden"
			}
			report, err := statusTestRun(t, a, out)
			if forbidden {
				if err == nil || report.Complete {
					t.Fatalf("unreadable candidate collection hidden: %+v", report)
				}
				continue
			}
			if err != nil || !report.Complete || len(report.Agents) != 0 || len(report.Candidates) != 1 || report.Candidates[0].Kind != kind {
				t.Fatalf("orphan candidate hidden without Agent API: %+v %v", report, err)
			}
		}
	}
}
