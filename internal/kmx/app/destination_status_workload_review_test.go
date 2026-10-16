package app

import (
	"errors"
	"strings"
	"testing"
)

func TestDestinationStatusReviewFailedRollout(t *testing.T) {
	for _, tc := range []struct{ name, typ, state, reason, want string }{
		{"deadline", "Progressing", "False", "ProgressDeadlineExceeded", "unhealthy"},
		{"replica-failure", "ReplicaFailure", "True", "FailedCreate", "unhealthy"},
		{"agent-not-ready-deadline", "Progressing", "False", "ProgressDeadlineExceeded", "unhealthy"},
		{"agent-not-ready-replica-failure", "ReplicaFailure", "True", "FailedCreate", "unhealthy"},
		{"progress", "Progressing", "True", "ReplicaSetUpdated", "pending"},
		{"initial-unavailable", "Available", "False", "MinimumReplicasUnavailable", "pending"},
		{"malformed", "Progressing", "invalid", "private-controller-reason", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, objects := statusTestKagentFixture(t)
			status := objects["Deployment"]["status"].(map[string]any)
			status["readyReplicas"] = 0
			status["availableReplicas"] = 0
			status["conditions"] = []any{map[string]any{"type": tc.typ, "status": tc.state, "reason": tc.reason, "message": "private-controller-message"}}
			if strings.HasPrefix(tc.name, "agent-not-ready") {
				c := objects["Agent"]["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)
				c["status"] = "False"
				c["reason"] = "DeploymentReady"
			}
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if report.Agents[0].Readiness.State != tc.want || err == nil || (tc.want == "pending") != errors.Is(err, ErrDestinationStatusPending) {
				t.Fatalf("want %s: %+v err=%v", tc.want, report.Agents[0], err)
			}
			if strings.Contains(report.Agents[0].Readiness.Reason, "private-controller") {
				t.Fatal("raw controller diagnostics exposed")
			}
		})
	}
}

func TestDestinationStatusReviewServiceTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target any
		omit   bool
		want   string
	}{
		{"numeric", 8080, false, "ready"},
		{"named", "http", false, "ready"},
		{"default", nil, true, "ready"},
		{"numeric-mismatch", 9999, false, "drifted"},
		{"unresolved-name", "missing", false, "drifted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, objects := statusTestKagentFixture(t)
			p := objects["Service"]["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)
			if !tc.omit {
				p["targetPort"] = tc.target
			}
			statusTestKagentRefresh(responses, objects)
			report, err := statusTestRun(t, a, out)
			if report.Agents[0].Readiness.State != tc.want || (err == nil) != (tc.want == "ready") {
				t.Fatalf("want %s: %+v err=%v", tc.want, report.Agents[0], err)
			}
		})
	}
}
