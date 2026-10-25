package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
)

func TestDestinationStatusSessionsExactRegistration(t *testing.T) {
	for _, scenario := range []string{"registered", "pending", "stale-uid", "stale-generation", "served-mismatch", "deleting", "replacement", "absent-api", "unreadable-discovery", "duplicate-condition", "null-condition", "unsafe-uid"} {
		t.Run(scenario, func(t *testing.T) {
			a, responses, out, _, paths := statusTestFixture(t)
			api := "/apis/" + agentsessions.AgentAPIVersion
			responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"agents.aks-durable-agents.io","versions":[{"groupVersion":"agents.aks-durable-agents.io/v1alpha1","version":"v1alpha1"}]}]}`
			responses[api] = `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"agents.aks-durable-agents.io/v1alpha1","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]}]}`
			digest := "sha256:" + strings.Repeat("a", 64)
			d := map[string]any{"apiVersion": agentsessions.AgentAPIVersion, "kind": "Agent", "metadata": map[string]any{"name": "greeter", "namespace": statusTestNS, "uid": "agent-uid", "generation": 1, "resourceVersion": "10"}, "spec": map[string]any{"package": map[string]any{"reference": "registry.example/suite@" + digest}, "member": "greeter", "modelBindingRef": map[string]any{"name": "default"}}, "status": map[string]any{"agentUID": "agent-uid", "observedGeneration": 1, "member": "greeter", "packageDigest": digest, "servedDigest": digest, "conditions": []any{map[string]any{"type": "PackageVerified", "status": "True", "observedGeneration": 1}, map[string]any{"type": "Registered", "status": "True", "observedGeneration": 1}}}}
			status := d["status"].(map[string]any)
			switch scenario {
			case "pending":
				delete(status, "conditions")
			case "stale-uid":
				status["agentUID"] = "old"
			case "stale-generation":
				status["observedGeneration"] = 0
			case "served-mismatch":
				status["servedDigest"] = "sha256:" + strings.Repeat("b", 64)
			case "duplicate-condition":
				status["conditions"] = append(status["conditions"].([]any), status["conditions"].([]any)[0])
			case "null-condition":
				status["conditions"] = []any{nil}
			case "unsafe-uid":
				d["metadata"].(map[string]any)["uid"] = "PRIVATE_STATUS_PAYLOAD\n"
			case "deleting":
				d["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-24T00:00:00Z"
			}
			raw, _ := json.Marshal(map[string]any{"apiVersion": agentsessions.AgentAPIVersion, "kind": "AgentList", "metadata": map[string]any{"resourceVersion": "10"}, "items": []any{d}})
			responses[api+"/namespaces/"+statusTestNS+"/agents?limit=128"] = string(raw)
			if scenario == "replacement" {
				d["metadata"].(map[string]any)["uid"] = "new"
			}
			raw, _ = json.Marshal(d)
			responses[api+"/namespaces/"+statusTestNS+"/agents/greeter"] = string(raw)
			if scenario == "absent-api" {
				responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","groups":[]}`
			}
			if scenario == "unreadable-discovery" {
				responses["/apis"] = "ERROR: denied"
			}
			err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, ExpectedRuntime: "agentsessions", Output: "json"})
			var report destinationStatusReport
			if json.Unmarshal(out.Bytes(), &report) != nil {
				t.Fatalf("no report: %s %v", out, err)
			}
			if scenario == "unreadable-discovery" {
				if err == nil || report.Complete {
					t.Fatal("unreadable discovery became success")
				}
				for _, runtime := range report.Runtimes {
					if runtime.Runtime == "agentsessions" && runtime.State != "unknown" {
						t.Fatalf("missing unknown state %+v", runtime)
					}
				}
				return
			}
			if scenario == "absent-api" {
				if err == nil || report.Complete {
					t.Fatal("missing API became success")
				}
				return
			}
			if len(report.Agents) != 1 {
				t.Fatalf("missing Agent: %+v %v", report, err)
			}
			if strings.Contains(out.String(), "PRIVATE_STATUS_PAYLOAD") {
				t.Fatal("unsafe resource identity leaked")
			}
			row := report.Agents[0]
			if (scenario == "duplicate-condition" || scenario == "null-condition") && (row.Readiness.State != "unknown" || report.Complete) {
				t.Fatalf("malformed condition became known pending: %+v", report)
			}
			if (row.Readiness.State == "registered") != (scenario == "registered") {
				t.Fatalf("false registration %+v", row)
			}
			if scenario == "registered" && (err != nil || !report.Complete || row.Member != "greeter" || row.SuiteDigest != digest || len(row.Resources) != 1 || len(row.Workloads) != 0) {
				t.Fatalf("report %+v %v", report, err)
			}
			if scenario != "registered" && err == nil {
				t.Fatalf("nonregistered exit %+v", row)
			}
			for _, path := range *paths {
				if strings.Contains(path, "modelconfig") || strings.Contains(path, "secret") || strings.Contains(path, "providers") {
					t.Fatalf("host internals read: %s", path)
				}
			}
		})
	}
}
