package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestDestinationStatusReadableAgentWithPartialIdentityIsNotDiscarded(t *testing.T) {
	for _, field := range []string{"uid", "resourceVersion"} {
		a, responses, out, _, _ := statusTestFixture(t)
		agent := statusTestObject("Agent", "readable-name")
		statusTestOwned(agent)
		statusTestAddAgent(t, responses, agent)
		delete(agent["metadata"].(map[string]any), field)
		responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = statusTestList(agent)
		report, err := statusTestRun(t, a, out)
		if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Name != "readable-name" || report.Agents[0].Readiness.State != "unknown" || report.Agents[0].Provenance.State != "unknown" {
			t.Fatalf("partial readable row dropped or guessed: %+v %v", report, err)
		}
	}
}

func TestDestinationStatusMalformedWorkloadStatusIsUnknownNotPending(t *testing.T) {
	a, responses, out, objects := statusTestKagentFixture(t)
	objects["Deployment"]["status"] = "private-malformed-status"
	statusTestKagentRefresh(responses, objects)
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" {
		t.Fatalf("malformed workload became pending: %+v %v", report, err)
	}
}

func TestDestinationStatusWorkloadAnchorsAreRechecked(t *testing.T) {
	a, responses, out, objects := statusTestKagentFixture(t)
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		err := capture(ctx, cmd)
		if strings.Contains(cmd.Args[len(cmd.Args)-1], "/pods?") {
			objects["Deployment"]["metadata"].(map[string]any)["uid"] = "replacement-deployment-uid"
			statusTestPut(responses, "/apis/apps/v1/namespaces/"+statusTestNS+"/deployments/greeter", objects["Deployment"])
		}
		return err
	}
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || report.Agents[0].Readiness.State != "unknown" {
		t.Fatalf("replaced workload reported current: %+v %v", report, err)
	}
}

func TestDestinationStatusUnsafeIdentityAndMalformedRecordNeverEscape(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "readable-name")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	agent["metadata"].(map[string]any)["uid"] = "private\nidentity-payload"
	agent["metadata"].(map[string]any)["annotations"].(map[string]any)[releaseLiftRecordAnnotation] = map[string]any{"unexpected": "private-record-payload"}
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = statusTestList(agent)
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Agents) != 1 {
		t.Fatalf("partial row lost or completed: %+v %v", report, err)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "identity-payload") || strings.Contains(string(raw), "private-record-payload") {
		t.Fatal("untrusted identity/record was printed")
	}
}
