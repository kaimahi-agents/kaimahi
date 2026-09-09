package app

import (
	"bytes"
	"testing"
)

func TestPodSummaryHonorsPodReadyCondition(t *testing.T) {
	pod := podStatus{}
	pod.Metadata.Name = "gated"
	pod.Status.Phase = "Running"
	pod.Status.Conditions = []statusCondition{{Type: "Ready", Status: "False"}}
	pod.Status.ContainerStatuses = []struct {
		RestartCount int `json:"restartCount"`
	}{{RestartCount: 2}}
	ready, restarts, rows := podSummary([]podStatus{pod})
	if ready != 0 || restarts != 2 || rows[0][1] != "no" {
		t.Fatalf("readiness gate ignored: ready=%d restarts=%d rows=%v", ready, restarts, rows)
	}
}

func TestStatusOutputValidation(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := a.StatusWithOptions(StatusOptions{Output: "toml"}); err == nil {
		t.Fatal("unsupported status output was accepted")
	}
}

func TestStatusReadinessIncludesInstalledOptionalRuntimes(t *testing.T) {
	if !statusReady(true, true, 2, 2, 1, 1, 2, 2) {
		t.Fatal("healthy installed runtimes were not ready")
	}
	if statusReady(true, true, 2, 2, 0, 1, 2, 2) {
		t.Fatal("unhealthy Ollama did not affect overall readiness")
	}
	if statusReady(true, true, 2, 2, 1, 1, 1, 2) {
		t.Fatal("unhealthy governance plane did not affect overall readiness")
	}
	if !statusReady(true, true, 2, 2, 0, 0, 0, 0) {
		t.Fatal("absent optional runtimes affected readiness")
	}
}

func TestTableAlignment(t *testing.T) {
	var out bytes.Buffer
	table(&out, []string{"NAME", "READY"}, [][]string{{"short", "yes"}, {"longer", "no"}})
	if got := out.String(); got != "  NAME    READY\n  short   yes  \n  longer  no   \n" {
		t.Fatalf("unexpected table:\n%q", got)
	}
}

func TestUnknownConditionsAreNotNegativeConditions(t *testing.T) {
	conditions := []statusCondition{{Type: "Ready", Status: "Unknown"}}
	if got := condition(conditions, "Ready"); got != "unknown" {
		t.Fatalf("unknown condition became %q", got)
	}
	pod := podStatus{}
	pod.Status.Conditions = conditions
	ready, _, rows := podSummary([]podStatus{pod})
	if ready != 0 || rows[0][1] != "unknown" {
		t.Fatalf("unknown pod counted or rendered as negative: %d %v", ready, rows)
	}
}
