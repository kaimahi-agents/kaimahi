package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestDestinationStatusMalformedResourceDiscoveryCannotBecomeAbsent(t *testing.T) {
	for _, entry := range []string{`{}`, `{"kind":"Widget","namespaced":true,"verbs":[]}`, `{"name":"widgets","namespaced":true,"verbs":[]}`, `{"name":"widgets","kind":"Widget","verbs":[]}`, `{"name":"widgets","kind":"Widget","namespaced":"true","verbs":[]}`, `{"name":"widgets","kind":"Widget","namespaced":true,"verbs":[42]}`} {
		a, responses, out, _, _ := statusTestFixture(t)
		responses[statusTestAPI] = `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"core.orka.ai/v1alpha1","resources":[` + entry + `]}`
		report, err := statusTestRun(t, a, out)
		if err == nil || report.Complete || report.Runtimes[0].State != "unknown" {
			t.Fatalf("malformed resource discovery became absence: %s %+v %v", entry, report, err)
		}
	}
}

func TestDestinationStatusMalformedPodStatesAreUnknownNotPending(t *testing.T) {
	for _, bad := range []any{42, "Bogus", nil} {
		for _, field := range []string{"condition", "container"} {
			a, responses, out, objects := statusTestKagentFixture(t)
			status := objects["Pod"]["status"].(map[string]any)
			if field == "condition" {
				status["conditions"].([]any)[0].(map[string]any)["status"] = bad
			} else {
				status["containerStatuses"].([]any)[0].(map[string]any)["ready"] = bad
			}
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" {
				t.Fatalf("invalid %s value %v became pending: %+v %v", field, bad, report, err)
			}
		}
	}
}

func TestDestinationStatusMalformedPodDomainValuesAreUnknown(t *testing.T) {
	for _, field := range []string{"phase", "nodeName", "spec"} {
		for _, bad := range []any{42, "Bogus", nil} {
			if field == "nodeName" && bad == "Bogus" {
				continue
			}
			a, responses, out, objects := statusTestKagentFixture(t)
			key := "status"
			if field == "nodeName" {
				key = "spec"
			}
			if field == "spec" {
				objects["Pod"]["spec"] = bad
			} else {
				objects["Pod"][key].(map[string]any)[field] = bad
			}
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" {
				t.Fatalf("invalid %s=%v became known placement: %+v %v", field, bad, report, err)
			}
		}
	}
}

func TestDestinationStatusMalformedWorkloadDeletionTimestampIsUnknown(t *testing.T) {
	for _, kind := range []string{"Deployment", "Service", "ReplicaSet", "Pod"} {
		for _, bad := range []any{42, "not-a-time"} {
			a, responses, out, objects := statusTestKagentFixture(t)
			objects[kind]["metadata"].(map[string]any)["deletionTimestamp"] = bad
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" {
				t.Fatalf("malformed %s deletion became known placement: %+v %v", kind, report, err)
			}
		}
	}
}

func TestDestinationStatusPartialWorkloadPagesRetainReadableIdentities(t *testing.T) {
	for _, kind := range []string{"ReplicaSet", "Pod"} {
		a, responses, out, objects := statusTestKagentFixture(t)
		prefix, plural, version := "/apis/apps/v1", "replicasets", "apps/v1"
		if kind == "Pod" {
			prefix, plural, version = "/api/v1", "pods", "v1"
		}
		path := prefix + "/namespaces/" + statusTestNS + "/" + plural + "?limit=128&labelSelector=app%3Dgreeter"
		responses[path] = strings.Replace(statusTestWorkloadList(version, kind, objects[kind]), `"continue":""`, `"continue":"next"`, 1)
		responses[path+"&continue=next"] = "ERROR:Forbidden: private-later-page"
		report, err := statusTestRun(t, a, out)
		if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Readiness.State != "unknown" {
			t.Fatalf("partial workload observation completed: %+v %v", report, err)
		}
		found := false
		for _, row := range report.Agents[0].Workloads {
			if row.Kind == kind && row.UID == strings.ToLower(kind)+"-uid" {
				found = true
				if row.Readiness.State != "unknown" {
					t.Fatalf("partial identity claimed ready: %+v", row)
				}
			}
		}
		if !found {
			t.Fatalf("readable %s identity discarded: %+v", kind, report.Agents[0].Workloads)
		}
		out.Reset()
		_ = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
		if !strings.Contains(out.String(), kind+"/"+objects[kind]["metadata"].(map[string]any)["name"].(string)) {
			t.Fatalf("table lost partial workload identity: %s", out)
		}
	}
}

func TestDestinationStatusTableIncludesProvenanceFailureReason(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	r := statusTestStamp(t, agent)
	r.Version = 99
	statusTestReplaceRecord(t, agent, r)
	statusTestAddAgent(t, responses, agent)
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Agents[0].Provenance.State != "unknown" {
		t.Fatal("invalid record accepted")
	}
	out.Reset()
	_ = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
	if !strings.Contains(out.String(), report.Agents[0].Provenance.Reason) {
		t.Fatalf("table omitted provenance failure reason: %s", out)
	}
}

func TestDestinationStatusInvalidGenerationIsNotTruncated(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	agent["metadata"].(map[string]any)["generation"] = 1.5
	statusTestAddAgent(t, responses, agent)
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" || report.Agents[0].Resources[0].Generation != 0 {
		t.Fatalf("invalid generation converted into a false integer: %+v %v", report, err)
	}
}

func TestDestinationStatusUnreadableNamespaceRecheckIsNotClaimedChanged(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	capture := a.captureCommand
	calls := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if cmd.Args[len(cmd.Args)-1] == "/api/v1/namespaces/"+statusTestNS {
			calls++
			if calls > 1 {
				responses["/api/v1/namespaces/"+statusTestNS] = "ERROR:Forbidden"
			}
		}
		return capture(ctx, cmd)
	}
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete {
		t.Fatal("unreadable recheck accepted")
	}
	raw, _ := json.Marshal(report.Errors)
	if strings.Contains(string(raw), "identity changed") {
		t.Fatalf("unknown observation inferred a changed identity: %s", raw)
	}
}
