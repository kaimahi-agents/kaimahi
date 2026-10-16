package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDestinationStatusMixedRuntimeInventoryIsNotFiltered(t *testing.T) {
	for _, expected := range []string{"", "orka"} {
		a, responses, out, _, _ := statusTestFixture(t)
		statusTestAddAgent(t, responses, statusTestObject("Agent", "greeter"))
		_, kagent, _, _ := statusTestKagentFixture(t)
		for path, response := range kagent {
			if path != "/apis" && !strings.HasPrefix(path, statusTestAPI) {
				responses[path] = response
			}
		}
		responses["/apis"] = `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"core.orka.ai","versions":[{"groupVersion":"core.orka.ai/v1alpha1","version":"v1alpha1"}]},{"name":"kagent.dev","versions":[{"groupVersion":"kagent.dev/v1alpha2","version":"v1alpha2"}]}]}`
		err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, ExpectedRuntime: expected, Output: "json"})
		var report destinationStatusReport
		if json.Unmarshal(out.Bytes(), &report) != nil {
			t.Fatal("no JSON report")
		}
		if len(report.Agents) != 2 || report.Agents[0].Runtime != "kagent" || report.Agents[1].Runtime != "orka" {
			t.Fatalf("runtime filtered or identities conflated: %+v %v", report, err)
		}
		if expected == "" {
			if err != nil || !report.Complete {
				t.Fatalf("mixed inventory refused without expectation: %+v %v", report, err)
			}
		} else if err == nil || report.Complete {
			t.Fatal("expectation mismatch hidden")
		}
	}
}

func TestDestinationStatusOnlyValidDiscoveryProvesNoRuntimeAgents(t *testing.T) {
	for _, raw := range []string{
		`{"apiVersion":"v1","kind":"APIGroupList","groups":[]}`,
		`{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"core.orka.ai","versions":[{"groupVersion":"core.orka.ai/v99","version":"v99"}]}]}`,
	} {
		a, responses, out, _, _ := statusTestFixture(t)
		responses["/apis"] = raw
		report, err := statusTestRun(t, a, out)
		absent := strings.Contains(raw, `"groups":[]`)
		if absent && (err != nil || !report.Complete || len(report.Agents) != 0 || report.Runtimes[0].State != "absent") {
			t.Fatalf("verified absence failed: %+v %v", report, err)
		}
		if !absent && (err == nil || report.Complete || report.Runtimes[0].State != "unknown") {
			t.Fatalf("unsupported version became absence: %+v %v", report, err)
		}
	}
}

func TestDestinationStatusPageBudgetStopsAndRetainsFourReadablePages(t *testing.T) {
	a, responses, out, _, paths := statusTestFixture(t)
	pages := map[string]string{}
	for i := 0; i < 4; i++ {
		agent := statusTestObject("Agent", fmt.Sprintf("page-%d", i))
		statusTestAddAgent(t, responses, agent)
		path := statusTestAPI + "/namespaces/" + statusTestNS + "/agents?limit=128"
		if i > 0 {
			path += fmt.Sprintf("&continue=page%d", i)
		}
		pages[path] = strings.Replace(statusTestList(agent), `"continue":""`, fmt.Sprintf(`"continue":"page%d"`, i+1), 1)
	}
	for path, response := range pages {
		responses[path] = response
	}
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Agents) != 4 {
		t.Fatalf("page budget lost rows or claimed completeness: %+v %v", report, err)
	}
	lists := 0
	for _, path := range *paths {
		if strings.Contains(path, "/agents?") {
			lists++
		}
	}
	if lists != 4 {
		t.Fatalf("unexpected list count: %d", lists)
	}
}

func TestDestinationStatusOversizedResponseIsUnknownWithoutPayload(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents?limit=128"] = `{"private":"` + strings.Repeat("x", 4<<20) + `"}`
	report, err := statusTestRun(t, a, out)
	if err == nil || report.Complete || len(report.Errors) == 0 {
		t.Fatalf("oversized response accepted: %+v %v", report, err)
	}
}

func TestDestinationStatusGETAndAcceptedByteBudgetsAreFinite(t *testing.T) {
	a, responses, _, _, paths := statusTestFixture(t)
	o := &destinationStatusObserver{a: a, ctx: context.Background(), calls: 2048}
	var object map[string]any
	if o.get("/apis", &object) || len(*paths) != 0 {
		t.Fatal("GET count exceeded")
	}
	o.calls = 0
	o.bytes = 63 << 20
	responses["/apis"] = `{"payload":"` + strings.Repeat("x", 2<<20) + `"}`
	if o.get("/apis", &object) || len(object) != 0 || o.calls != 1 {
		t.Fatal("over-budget response accepted or decoded")
	}
	if o.get("/apis", &object) || o.calls != 1 {
		t.Fatal("byte budget did not stop subsequent requests")
	}
}
