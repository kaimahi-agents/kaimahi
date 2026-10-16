package app

import (
	"context"
	"os/exec"
	"testing"
)

func TestDestinationStatusFreezesApplicationContext(t *testing.T) {
	a, _, out, _, _ := statusTestFixture(t)
	capture := a.captureCommand
	changed := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		err := capture(ctx, cmd)
		if !changed {
			changed = true
			a.Cfg.KubeContext = "changed-after-first-GET"
		}
		return err
	}
	report, err := statusTestRun(t, a, out)
	if err != nil || report.Target.Context != "production" || !report.Complete {
		t.Fatalf("target not frozen: %+v %v", report, err)
	}
}

func TestDestinationStatusResourceVersionChurnIsNotSpecDrift(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestAddAgent(t, responses, agent)
	agent["metadata"].(map[string]any)["resourceVersion"] = "11"
	statusTestPut(responses, statusTestAPI+"/namespaces/"+statusTestNS+"/agents/greeter", agent)
	report, err := statusTestRun(t, a, out)
	if err != nil || !report.Complete || report.Agents[0].Readiness.State != "ready" {
		t.Fatalf("status-only churn treated as drift: %+v %v", report, err)
	}
}

func TestDestinationStatusRechecksCurrentReadinessWithoutRetry(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestAddAgent(t, responses, agent)
	agent["metadata"].(map[string]any)["resourceVersion"] = "11"
	agent["status"].(map[string]any)["ready"] = false
	agent["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = "False"
	statusTestPut(responses, statusTestAPI+"/namespaces/"+statusTestNS+"/agents/greeter", agent)
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Agents[0].Readiness.State != "unhealthy" {
		t.Fatalf("stale Ready claim retained: %+v %v", report, err)
	}
}
