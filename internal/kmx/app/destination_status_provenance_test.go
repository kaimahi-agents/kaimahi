package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func statusTestOwned(doc map[string]any) {
	doc["metadata"].(map[string]any)["annotations"] = (&orkaSuiteOwnership{Suite: "release-notes", Digest: "sha256:" + strings.Repeat("1", 64), Member: "greeter", Renderer: "kmx.native-orka/v2", BindingDigest: strings.Repeat("2", 64), RenderedDigest: strings.Repeat("3", 64)}).markers()
	doc["metadata"].(map[string]any)["labels"] = map[string]any{"app.kubernetes.io/managed-by": "kmx"}
}
func statusTestStamp(t *testing.T, doc map[string]any) releaseLiftProvenance {
	t.Helper()
	kind := doc["kind"].(string)
	digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	if err != nil {
		t.Fatal(err)
	}
	r := releaseLiftProvenance{Version: 1, LiftID: strings.Repeat("ab", 16), SuiteDigest: "sha256:" + strings.Repeat("1", 64), ReceiptDigest: "sha256:" + strings.Repeat("4", 64), ReceiptRunID: strings.Repeat("cd", 16), Evaluation: ReleaseLiftTarget{Context: "dev", ClusterUID: "dev-cluster", Namespace: "dev", NamespaceUID: "dev-namespace"}, EvaluationName: "dev", Target: ReleaseLiftTarget{Context: "different-operator-alias", ClusterUID: "cluster-uid", Namespace: statusTestNS, NamespaceUID: "namespace-uid"}, Gate: "promotion", EvaluatedOnTarget: false, AttemptedAt: "2026-10-16T12:20:00Z", ResourceSpecs: map[string]string{"Provider": strings.Repeat("5", 64), "Agent": strings.Repeat("6", 64)}}
	r.ResourceSpecs[kind] = digest
	statusTestReplaceRecord(t, doc, r)
	return r
}
func statusTestReplaceRecord(t *testing.T, doc map[string]any, r releaseLiftProvenance) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	meta := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
		meta["annotations"] = ann
	}
	ann[releaseLiftRecordAnnotation] = string(raw)
}

func TestDestinationStatusThreeProvenanceStatesAndRecordedClaim(t *testing.T) {
	for _, tc := range []struct {
		name, state           string
		owned, stamp, partial bool
	}{
		{"foreign", "not kmx-owned", false, false, false},
		{"foreign-copied-annotation", "not kmx-owned", false, true, false},
		{"owned-before-4c", "provenance not recorded", true, false, false},
		{"partial-ownership", "unknown", false, false, true},
		{"owned-recorded", "recorded", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent := statusTestObject("Agent", "greeter")
			if tc.owned {
				statusTestOwned(agent)
			}
			if tc.partial {
				agent["metadata"].(map[string]any)["annotations"] = map[string]any{orkaSuiteMarkers[0]: "release-notes"}
			}
			statusTestAddAgent(t, responses, agent)
			if tc.stamp {
				statusTestStamp(t, agent)
				statusTestAddAgent(t, responses, agent)
			}
			report, err := statusTestRun(t, a, out)
			if len(report.Agents) != 1 || report.Agents[0].Provenance.State != tc.state {
				t.Fatalf("states collapsed: %+v %v", report, err)
			}
			if tc.state == "unknown" {
				if err == nil || report.Complete {
					t.Fatal("partial ownership reported complete")
				}
				return
			}
			if err != nil || !report.Complete {
				t.Fatalf("valid observation failed: %+v %v", report, err)
			}
			row := report.Agents[0]
			if tc.owned && (row.Member != "greeter" || row.SuiteDigest != "sha256:"+strings.Repeat("1", 64)) {
				t.Fatalf("suite identity lost: %+v", row)
			}
			if tc.state == "recorded" {
				if row.Provenance.Record == nil || row.Provenance.Record.Gate != "promotion" || row.Provenance.Record.EvaluatedOnTarget || row.Provenance.Record.Actor != nil {
					t.Fatalf("fabricated or lost claim: %+v", row.Provenance)
				}
				if len(row.Resources) != 2 || row.Resources[1].Provenance.State != "provenance not recorded" {
					t.Fatalf("companion provenance inferred: %+v", row.Resources)
				}
			} else if row.Provenance.Record != nil {
				t.Fatal("missing provenance was inferred")
			}
		})
	}
}

func TestDestinationStatusInvalidAnnotationFailsClosedWithoutLeakingPayload(t *testing.T) {
	for _, change := range []string{"missing-receipt", "missing-disclosure", "version", "duplicate", "actor", "target", "suite", "gate", "time", "spec", "missing-spec", "unknown-field"} {
		t.Run(change, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent := statusTestObject("Agent", "greeter")
			statusTestOwned(agent)
			statusTestAddAgent(t, responses, agent)
			r := statusTestStamp(t, agent)
			switch change {
			case "missing-receipt":
				r.ReceiptDigest = ""
			case "version":
				r.Version = 2
			case "actor":
				r.Actor = "private\nannotation-payload"
			case "target":
				r.Target.NamespaceUID = "replacement-namespace"
			case "suite":
				r.SuiteDigest = "sha256:" + strings.Repeat("7", 64)
			case "gate":
				r.EvaluatedOnTarget = true
			case "time":
				r.AttemptedAt = "2026-10-16T12:20:00+01:00"
			case "spec":
				r.ResourceSpecs["Agent"] = "malformed"
			case "missing-spec":
				delete(r.ResourceSpecs, "Provider")
			}
			statusTestReplaceRecord(t, agent, r)
			ann := agent["metadata"].(map[string]any)["annotations"].(map[string]any)
			if change == "duplicate" {
				ann[releaseLiftRecordAnnotation] = strings.Replace(ann[releaseLiftRecordAnnotation].(string), `"version":1`, `"version":1,"version":1`, 1)
			}
			if change == "missing-disclosure" {
				ann[releaseLiftRecordAnnotation] = strings.Replace(ann[releaseLiftRecordAnnotation].(string), `"evaluatedOnTarget":false,`, "", 1)
			}
			if change == "unknown-field" {
				ann[releaseLiftRecordAnnotation] = strings.TrimSuffix(ann[releaseLiftRecordAnnotation].(string), "}") + `,"credential":"private-annotation-payload"}`
			}
			statusTestAddAgent(t, responses, agent)
			report, err := statusTestRun(t, a, out)
			if err == nil || report.Complete || len(report.Agents) != 1 || report.Agents[0].Provenance.State != "unknown" || report.Agents[0].Suite != "release-notes" {
				t.Fatalf("invalid claim accepted or readable identity lost: %+v %v", report, err)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "annotation-payload") {
				t.Fatal("unsafe annotation was printed")
			}
		})
	}
}

func TestDestinationStatusCandidatesAreNotDeployedAgents(t *testing.T) {
	for _, run := range []string{strings.Repeat("ab", 16), "malformed-run"} {
		a, responses, out, _, _ := statusTestFixture(t)
		agent := statusTestObject("Agent", "candidate")
		statusTestOwned(agent)
		agent["metadata"].(map[string]any)["annotations"].(map[string]any)[suiteCandidateRunMarker] = run
		statusTestAddAgent(t, responses, agent)
		report, err := statusTestRun(t, a, out)
		if len(report.Agents) != 0 || len(report.Candidates) != 1 {
			t.Fatalf("candidate counted as deployed: %+v", report)
		}
		if run == "malformed-run" && (err == nil || report.Complete || report.Candidates[0].CandidateRun != nil) {
			t.Fatal("malformed candidate became deployed or complete")
		}
	}
}
