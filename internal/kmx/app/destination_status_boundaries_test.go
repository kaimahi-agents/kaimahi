package app

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestDestinationStatusListBoundsAndSnapshotUncertaintyPreserveRows(t *testing.T) {
	for _, tc := range []string{"snapshot-change", "repeat-token", "duplicate-name", "missing-items", "null-items", "wrong-version", "missing-version", "too-many-items", "case-folded-items"} {
		t.Run(tc, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			first := statusTestObject("Agent", "first")
			second := statusTestObject("Agent", "second")
			statusTestAddAgent(t, responses, first)
			statusTestAddAgent(t, responses, second)
			firstPage := strings.Replace(statusTestList(first), `"continue":""`, `"continue":"next"`, 1)
			page := statusTestList(second)
			switch tc {
			case "snapshot-change":
				page = strings.ReplaceAll(page, `"resourceVersion":"10"`, `"resourceVersion":"11"`)
			case "repeat-token":
				page = strings.Replace(page, `"continue":""`, `"continue":"next"`, 1)
			case "duplicate-name":
				page = statusTestList(first)
			case "missing-items":
				page = strings.Replace(page, `"items":[`, `"ignored":[`, 1)
			case "null-items":
				page = `{"kind":"AgentList","apiVersion":"core.orka.ai/v1alpha1","metadata":{"resourceVersion":"10"},"items":null}`
			case "wrong-version":
				page = strings.Replace(page, `"apiVersion":"core.orka.ai/v1alpha1"`, `"apiVersion":"foreign/v1"`, 1)
			case "missing-version":
				page = strings.Replace(page, `"apiVersion":"core.orka.ai/v1alpha1",`, "", 1)
			case "case-folded-items":
				page = strings.Replace(page, `"items":`, `"Items":`, 1)
			case "too-many-items":
				items := make([]map[string]any, 129)
				for i := range items {
					items[i] = second
				}
				page = statusTestList(items...)
			}
			responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = firstPage
			responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128&continue=next"] = page
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || len(report.Agents) == 0 || report.Agents[0].Name != "first" {
				t.Fatalf("lost readable rows or accepted incomplete snapshot: %+v %v", report, err)
			}
		})
	}
}

func TestDestinationStatusCaseFoldedDiscoveryCannotProveEmpty(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","Groups":[]}`
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete {
		t.Fatalf("case-folded discovery proved absence: %+v %v", report, err)
	}
}

func TestDestinationStatusRuntimeExpectationNeverFiltersOtherAgents(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	statusTestAddAgent(t, responses, statusTestObject("Agent", "other-runtime"))
	err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, ExpectedRuntime: "kagent", Output: "json"})
	var report destinationStatusReport
	if json.Unmarshal(out.Bytes(), &report) != nil {
		t.Fatal("no report")
	}
	if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Runtime != "orka" {
		t.Fatalf("runtime mismatch filtered or hidden: %+v %v", report, err)
	}
}

func TestDestinationStatusOriginalAgentAndNamespaceMustRemainObservable(t *testing.T) {
	for _, change := range []string{"agent-uid", "agent-generation", "namespace-uid"} {
		t.Run(change, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent := statusTestObject("Agent", "greeter")
			statusTestAddAgent(t, responses, agent)
			if change == "agent-uid" || change == "agent-generation" {
				if change == "agent-uid" {
					agent["metadata"].(map[string]any)["uid"] = "changed"
				} else {
					agent["metadata"].(map[string]any)["generation"] = 2
				}
				statusTestPut(responses, statusTestAPI+"/namespaces/"+statusTestNS+"/agents/greeter", agent)
			} else {
				capture := a.captureCommand
				calls := 0
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if cmd.Args[len(cmd.Args)-1] == "/api/v1/namespaces/"+statusTestNS {
						calls++
						if calls > 1 {
							responses["/api/v1/namespaces/"+statusTestNS] = `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"workloads","uid":"replacement-namespace"}}`
						}
					}
					return capture(ctx, cmd)
				}
			}
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || len(report.Agents) != 1 {
				t.Fatalf("changed target/primary reported current: %+v %v", report, err)
			}
		})
	}
}

func TestDestinationStatusCancelledObservationStillEmitsUnknownReport(t *testing.T) {
	a, _, out, _, paths := statusTestFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.Run.Context = ctx
	report, err := statusTestRun(t, a, out)
	if !errors.Is(err, context.Canceled) || report.Complete || len(*paths) != 0 {
		t.Fatalf("cancelled inspection continued: %+v %v %v", report, err, *paths)
	}
}

func TestDestinationStatusForeignPrerequisiteCannotSatisfyOwnedPlacement(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent := statusTestObject("Agent", "greeter")
	statusTestOwned(agent)
	statusTestAddAgent(t, responses, agent)
	statusTestPut(responses, statusTestAPI+"/namespaces/"+statusTestNS+"/providers/greeter", statusTestObject("Provider", "greeter"))
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Agents[0].Readiness.State != "drifted" || report.Agents[0].Resources[1].Provenance.State != "not kmx-owned" {
		t.Fatalf("foreign prerequisite accepted or rewritten: %+v %v", report, err)
	}
}
