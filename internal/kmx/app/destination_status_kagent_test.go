package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const statusTestKagentAPI = "/apis/kagent.dev/v1alpha2"

func statusTestOwnerRef(version, kind, name, uid string) []any {
	return []any{map[string]any{"apiVersion": version, "kind": kind, "name": name, "uid": uid, "controller": true}}
}
func statusTestWorkloadList(version, kind string, items ...map[string]any) string {
	if items == nil {
		items = []map[string]any{}
	}
	raw, _ := json.Marshal(map[string]any{"apiVersion": version, "kind": kind + "List", "metadata": map[string]any{"resourceVersion": "20", "continue": ""}, "items": items})
	return string(raw)
}
func statusTestPut(responses map[string]string, path string, doc any) {
	raw, _ := json.Marshal(doc)
	responses[path] = string(raw)
}
func statusTestKagentFixture(t *testing.T) (*App, map[string]string, *bytes.Buffer, map[string]map[string]any) {
	t.Helper()
	a, responses, out, _, _ := statusTestFixture(t)
	responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"kagent.dev","versions":[{"groupVersion":"kagent.dev/v1alpha2","version":"v1alpha2"}]}]}`
	responses[statusTestKagentAPI] = `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"kagent.dev/v1alpha2","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]}]}`
	objects := map[string]map[string]any{}
	for _, kind := range []string{"Agent", "ModelConfig", "Deployment", "Service", "ReplicaSet", "Pod"} {
		d := statusTestObject(kind, "greeter")
		d["apiVersion"] = "kagent.dev/v1alpha2"
		m := d["metadata"].(map[string]any)
		m["uid"] = strings.ToLower(kind) + "-uid"
		if kind == "Agent" || kind == "ModelConfig" {
			statusTestOwned(d)
			m["annotations"].(map[string]any)[orkaSuiteMarkers[3]] = "kmx.native-kagent/v2"
			d["status"] = map[string]any{"observedGeneration": 1, "conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": 1}, map[string]any{"type": "Ready", "status": "True", "reason": "DeploymentReady", "observedGeneration": 1}}}
			if kind == "ModelConfig" {
				d["status"] = map[string]any{"observedGeneration": 1, "conditions": []any{map[string]any{"type": "Accepted", "status": "True"}}}
			}
		}
		objects[kind] = d
	}
	agent := objects["Agent"]
	agent["spec"] = map[string]any{"type": "Declarative", "declarative": map[string]any{"runtime": "go", "modelConfig": "greeter", "systemMessage": "private-authored-message"}}
	deployment := objects["Deployment"]
	deployment["apiVersion"] = "apps/v1"
	meta := deployment["metadata"].(map[string]any)
	meta["ownerReferences"] = statusTestOwnerRef("kagent.dev/v1alpha2", "Agent", "greeter", "agent-uid")
	meta["annotations"] = map[string]any{"deployment.kubernetes.io/revision": "1"}
	template := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "greeter"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "kagent", "image": "test.invalid/kagent:fixture", "ports": []any{map[string]any{"name": "http", "containerPort": 8080, "protocol": "TCP"}}}}}}
	deployment["spec"] = map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": map[string]any{"app": "greeter"}}, "template": template}
	deployment["status"] = map[string]any{"observedGeneration": 1, "replicas": 1, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 0}
	rs := objects["ReplicaSet"]
	rs["apiVersion"] = "apps/v1"
	meta = rs["metadata"].(map[string]any)
	meta["name"] = "greeter-rs"
	meta["ownerReferences"] = statusTestOwnerRef("apps/v1", "Deployment", "greeter", "deployment-uid")
	meta["annotations"] = map[string]any{"deployment.kubernetes.io/revision": "1"}
	meta["labels"] = map[string]any{"app": "greeter"}
	// Decode a fresh copy: mutations must not change the Deployment's template.
	raw, _ := json.Marshal(deployment["spec"])
	var rsSpec map[string]any
	_ = json.Unmarshal(raw, &rsSpec)
	rs["spec"] = rsSpec
	rs["status"] = map[string]any{"observedGeneration": 1, "replicas": 1, "readyReplicas": 1, "availableReplicas": 1}
	service := objects["Service"]
	service["apiVersion"] = "v1"
	service["metadata"].(map[string]any)["ownerReferences"] = statusTestOwnerRef("kagent.dev/v1alpha2", "Agent", "greeter", "agent-uid")
	service["spec"] = map[string]any{"selector": map[string]any{"app": "greeter"}, "ports": []any{map[string]any{"port": 8080, "protocol": "TCP"}}}
	pod := objects["Pod"]
	pod["apiVersion"] = "v1"
	meta = pod["metadata"].(map[string]any)
	meta["name"] = "greeter-pod"
	meta["labels"] = map[string]any{"app": "greeter"}
	meta["ownerReferences"] = statusTestOwnerRef("apps/v1", "ReplicaSet", "greeter-rs", "replicaset-uid")
	pod["spec"] = map[string]any{"nodeName": "fixture-node", "containers": []any{map[string]any{"name": "kagent", "image": "test.invalid/kagent:fixture", "ports": []any{map[string]any{"name": "http", "containerPort": 8080, "protocol": "TCP"}}}}}
	pod["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "containerStatuses": []any{map[string]any{"name": "kagent", "ready": true}}}
	statusTestKagentRefresh(responses, objects)
	return a, responses, out, objects
}
func statusTestKagentRefresh(responses map[string]string, objects map[string]map[string]any) {
	statusTestPut(responses, statusTestKagentAPI+"/namespaces/"+statusTestNS+"/agents/greeter", objects["Agent"])
	responses[statusTestKagentAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = statusTestWorkloadList("kagent.dev/v1alpha2", "Agent", objects["Agent"])
	statusTestPut(responses, statusTestKagentAPI+"/namespaces/"+statusTestNS+"/modelconfigs/greeter", objects["ModelConfig"])
	statusTestPut(responses, "/apis/apps/v1/namespaces/"+statusTestNS+"/deployments/greeter", objects["Deployment"])
	statusTestPut(responses, "/api/v1/namespaces/"+statusTestNS+"/services/greeter", objects["Service"])
	responses["/apis/apps/v1/namespaces/"+statusTestNS+"/replicasets?limit=128&labelSelector=app%3Dgreeter"] = statusTestWorkloadList("apps/v1", "ReplicaSet", objects["ReplicaSet"])
	responses["/api/v1/namespaces/"+statusTestNS+"/pods?limit=128&labelSelector=app%3Dgreeter"] = statusTestWorkloadList("v1", "Pod", objects["Pod"])
	statusTestPut(responses, "/apis/apps/v1/namespaces/"+statusTestNS+"/replicasets/greeter-rs", objects["ReplicaSet"])
	statusTestPut(responses, "/api/v1/namespaces/"+statusTestNS+"/pods/greeter-pod", objects["Pod"])
}

func TestDestinationStatusKagentCurrentOwnedRolloutAndGenerationRules(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		complete    bool
		mutate      func(map[string]map[string]any)
	}{
		{"ready", "ready", true, func(map[string]map[string]any) {}},
		{"stale-agent", "pending", true, func(o map[string]map[string]any) { o["Agent"]["status"].(map[string]any)["observedGeneration"] = 0 }},
		{"rejected-agent", "unhealthy", true, func(o map[string]map[string]any) {
			o["Agent"]["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "False"
		}},
		{"wrong-ready-reason", "pending", true, func(o map[string]map[string]any) {
			o["Agent"]["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["reason"] = "NotDeploymentReady"
		}},
		{"stale-modelconfig", "pending", true, func(o map[string]map[string]any) {
			o["ModelConfig"]["status"].(map[string]any)["observedGeneration"] = 0
		}},
		{"foreign-deployment-uid", "drifted", true, func(o map[string]map[string]any) {
			o["Deployment"]["metadata"].(map[string]any)["ownerReferences"] = statusTestOwnerRef("kagent.dev/v1alpha2", "Agent", "greeter", "replaced-agent-uid")
		}},
		{"foreign-service-uid", "drifted", true, func(o map[string]map[string]any) {
			o["Service"]["metadata"].(map[string]any)["ownerReferences"] = statusTestOwnerRef("kagent.dev/v1alpha2", "Agent", "greeter", "replaced-agent-uid")
		}},
		{"foreign-pod-owner", "drifted", true, func(o map[string]map[string]any) {
			o["Pod"]["metadata"].(map[string]any)["ownerReferences"] = statusTestOwnerRef("apps/v1", "ReplicaSet", "greeter-rs", "foreign-rs-uid")
		}},
		{"service-selector", "drifted", true, func(o map[string]map[string]any) {
			o["Service"]["spec"].(map[string]any)["selector"] = map[string]any{"app": "foreign"}
		}},
		{"deployment-not-current", "pending", true, func(o map[string]map[string]any) {
			o["Deployment"]["status"].(map[string]any)["observedGeneration"] = 0
		}},
		{"replicaset-template", "drifted", true, func(o map[string]map[string]any) {
			o["ReplicaSet"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "test.invalid/kagent:foreign"
		}},
		{"pod-not-ready", "pending", true, func(o map[string]map[string]any) {
			o["Pod"]["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "False"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, objects := statusTestKagentFixture(t)
			tc.mutate(objects)
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if len(report.Agents) != 1 || report.Agents[0].Readiness.State != tc.state || report.Complete != tc.complete {
				t.Fatalf("kagent readiness=%+v, err=%v", report, err)
			}
			if (tc.state == "pending") != errors.Is(err, ErrDestinationStatusPending) || tc.state != "ready" && err == nil {
				t.Fatalf("wrong exit category: %v", err)
			}
			if tc.state == "ready" && (err != nil || len(report.Agents[0].Workloads) != 4 || report.Agents[0].Provenance.State != "provenance not recorded") {
				t.Fatalf("incomplete owned rollout: %+v %v", report, err)
			}
		})
	}
}

func TestDestinationStatusKagentWorkloadReadFailureKeepsPrimary(t *testing.T) {
	a, responses, out, _ := statusTestKagentFixture(t)
	responses["/api/v1/namespaces/"+statusTestNS+"/pods?limit=128&labelSelector=app%3Dgreeter"] = "ERROR:Forbidden private-pod-body"
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Provenance.State != "provenance not recorded" || report.Agents[0].Readiness.State != "unknown" {
		t.Fatalf("workload uncertainty hidden: %+v %v", report, err)
	}
}

func TestDestinationStatusKagentRecordedSpecUsesKagentHash(t *testing.T) {
	a, responses, out, objects := statusTestKagentFixture(t)
	agent := objects["Agent"]
	r := statusTestStamp(t, agent)
	agentHash, _ := kagentSpecFingerprint(agent["spec"])
	modelHash, _ := kagentSpecFingerprint(objects["ModelConfig"]["spec"])
	r.ResourceSpecs = map[string]string{"Agent": agentHash, "ModelConfig": modelHash}
	statusTestReplaceRecord(t, agent, r)
	statusTestKagentRefresh(responses, objects)
	report, err := statusTestRun(t, a, out)
	if err != nil || !report.Complete || report.Agents[0].Provenance.State != "recorded" || report.Agents[0].Resources[0].SpecDrift != "matched" {
		t.Fatalf("kagent spec hash mismatch: %+v %v", report, err)
	}
}
