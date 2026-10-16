package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestDestinationStatusUnsafeOwnershipAnnotationsAreSuppressed(t *testing.T) {
	for _, shape := range []struct{ name, value string }{
		{"openai-api-key", "sk-" + strings.Repeat("a", 24)},
		{"azure-openai-key-hex", strings.Repeat("a1b2", 8)},
	} {
		if scaffold.ValidateObjectName(shape.value) != nil || statusSafeText(shape.value) {
			t.Fatal("fixture must be DNS-valid but unsafe for status output")
		}
		if match := secretshapes.Match(shape.value); match == nil || match.Name != shape.name {
			t.Fatal("fixture must match the intended credential shape")
		}
		for _, runtime := range []string{"orka", "kagent"} {
			for _, marker := range []struct{ name, key string }{{"suite", orkaSuiteMarkers[0]}, {"member", orkaSuiteMarkers[2]}} {
				for _, placement := range []string{"deployed", "candidate"} {
					t.Run(shape.name+"/"+runtime+"/"+marker.name+"/"+placement, func(t *testing.T) {
						a, responses, out, _, _ := statusTestFixture(t)
						agent := statusTestObject("Agent", "greeter")
						statusTestOwned(agent)
						var objects map[string]map[string]any
						if runtime == "kagent" {
							a, responses, out, objects = statusTestKagentFixture(t)
							agent = objects["Agent"]
						}
						meta := agent["metadata"].(map[string]any)
						ann := meta["annotations"].(map[string]any)
						ann[marker.key] = shape.value
						if placement == "candidate" {
							ann[suiteCandidateRunMarker] = strings.Repeat("ab", 16)
						}
						// No Lift record: ownership names must be safe independently.
						if runtime == "kagent" {
							statusTestKagentRefresh(responses, objects)
						} else {
							statusTestAddAgent(t, responses, agent)
						}
						if owner, state := statusOwnership(runtime, meta); owner != nil || state != "unknown" {
							t.Error("unsafe ownership was accepted")
						}
						err := a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "json"})
						var report destinationStatusReport
						if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
							t.Fatalf("decode report: %v", decodeErr)
						}
						if !errors.Is(err, errDestinationStatus) || report.Complete {
							t.Error("unsafe ownership did not make observation incomplete")
						}
						rows, other := report.Agents, report.Candidates
						if placement == "candidate" {
							rows, other = report.Candidates, report.Agents
						}
						if len(rows) != 1 || len(other) != 0 {
							t.Fatal("readable row lost or candidate segregation changed")
						}
						row := rows[0]
						if row.Suite != "" || row.Member != "" || row.Provenance.State != "unknown" || row.Provenance.Record != nil {
							t.Error("unsafe ownership populated suite/member or provenance")
						}
						if strings.Contains(out.String(), shape.value) {
							t.Error("credential-shaped annotation escaped in JSON")
						}
						out.Reset()
						err = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
						if !errors.Is(err, errDestinationStatus) {
							t.Error("table changed the incomplete observation result")
						}
						if strings.Contains(out.String(), shape.value) {
							t.Error("credential-shaped annotation escaped in table")
						}
					})
				}
			}
		}
	}
}

func TestDestinationStatusCandidateTableIncludesProvenanceReason(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"incomplete-ownership", "native suite ownership is incomplete, conflicting or malformed"},
		{"invalid-lift-record", "Lift observation is malformed, unsupported or incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent := statusTestObject("Agent", "candidate")
			statusTestOwned(agent)
			ann := agent["metadata"].(map[string]any)["annotations"].(map[string]any)
			ann[suiteCandidateRunMarker] = strings.Repeat("ab", 16)
			if tc.name == "incomplete-ownership" {
				delete(ann, orkaSuiteMarkers[2])
			} else {
				ann[releaseLiftRecordAnnotation] = "private-malformed-record-payload"
			}
			statusTestAddAgent(t, responses, agent)
			report, err := statusTestRun(t, a, out)
			if !errors.Is(err, errDestinationStatus) || report.Complete || len(report.Agents) != 0 || len(report.Candidates) != 1 {
				t.Fatal("uncertain candidate lost, counted as deployed or reported complete")
			}
			candidate := report.Candidates[0]
			if candidate.Provenance.State != "unknown" || candidate.Provenance.Reason != tc.reason || candidate.Provenance.Record != nil {
				t.Fatalf("JSON lost the fixed candidate provenance diagnostic: %+v", candidate.Provenance)
			}
			if candidate.Readiness.State != "not applicable" {
				t.Fatal("candidate readiness was not segregated")
			}
			out.Reset()
			err = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
			if !errors.Is(err, errDestinationStatus) {
				t.Fatal("table changed the incomplete observation result")
			}
			if !strings.Contains(out.String(), candidate.Provenance.Reason) {
				t.Fatalf("table omitted JSON candidate provenance reason: %s", out)
			}
			if strings.Contains(out.String(), "private-malformed-record-payload") {
				t.Fatal("table exposed the invalid record body")
			}
		})
	}
}
