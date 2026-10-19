package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDestinationStatusVersionedAttemptReader(t *testing.T) {
	doc := statusTestObject("Agent", "greeter")
	r := statusTestStamp(t, doc)
	for _, tc := range []struct {
		version  int
		runtime  string
		accepted bool
	}{
		{1, "orka", true}, {2, "orka", true}, {3, "orka", false}, {2, "kagent", false},
	} {
		r.Version = tc.version
		raw, _ := json.Marshal(r)
		if got := statusRecord(string(raw), tc.runtime); (got != nil) != tc.accepted {
			t.Errorf("version=%d runtime=%s accepted=%v want=%v", tc.version, tc.runtime, got != nil, tc.accepted)
		}
		if got := frozenV081StatusRecord(string(raw), tc.runtime); (got != nil) != (tc.version == 1 && tc.runtime == "orka") {
			t.Errorf("frozen v0.8.1 reader credited unsupported version %d", tc.version)
		}
	}
}

// The real observer runs against complete API documents. Only the exact shared
// ConfigMap transport is intercepted; any broader read or mutation fails in the
// existing raw-GET fixture.
func statusUpdateRecordGET(t *testing.T, a *App, wire *string, fail *bool) *int {
	t.Helper()
	prior := a.captureCommand
	reads := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "configmap") {
			want := []string{"--context", "production", "--request-timeout=10s", "-n", statusTestNS, "get", "configmap", releaseStateName("release-notes", "greeter", "orka"), "--ignore-not-found", "-o", "json"}
			if !slices.Equal(cmd.Args[1:], want) {
				t.Fatalf("not an exact bounded record GET: %v", cmd.Args)
			}
			if raw, _ := io.ReadAll(cmd.Stdin); len(raw) != 0 {
				t.Fatal("GET had input")
			}
			reads++
			if *fail {
				_, _ = io.WriteString(cmd.Stderr, "Forbidden private-record-payload")
				return errors.New("denied")
			}
			_, err := io.WriteString(cmd.Stdout, *wire)
			return err
		}
		return prior(ctx, cmd)
	}
	return &reads
}

func statusUpdatePair(t *testing.T, version int) (map[string]any, map[string]any, releaseState) {
	t.Helper()
	agent, provider := statusTestObject("Agent", "greeter"), statusTestObject("Provider", "greeter")
	agent["spec"].(map[string]any)["providerRef"] = map[string]any{"name": "greeter", "namespace": statusTestNS}
	statusTestOwned(agent)
	statusTestOwned(provider)
	r := statusTestStamp(t, agent)
	r.Version = version
	for _, doc := range []map[string]any{provider, agent} {
		digest, err := suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
		if err != nil {
			t.Fatal(err)
		}
		r.ResourceSpecs[doc["kind"].(string)] = digest
	}
	statusTestReplaceRecord(t, agent, r)
	statusTestReplaceRecord(t, provider, r)
	state := releaseState{Version: 1, Suite: "release-notes", Member: "greeter", Runtime: "orka", Target: r.Target, OriginLiftID: r.LiftID}
	for _, doc := range []map[string]any{provider, agent} {
		proof, err := releaseObjectFingerprint(doc, "greeter-uid", 1)
		if err != nil {
			t.Fatal(err)
		}
		state.Baseline = append(state.Baseline, proof)
	}
	state.History = []releaseStateRevision{{LiftID: r.LiftID, SuiteDigest: r.SuiteDigest, EvidenceDigest: r.ReceiptDigest, State: "completed", Objects: append([]releaseStateObject(nil), state.Baseline...)}}
	return agent, provider, state
}

func statusUpdateInstall(t *testing.T, responses map[string]string, agent, provider map[string]any) {
	t.Helper()
	statusTestAddAgent(t, responses, agent)
	raw, _ := json.Marshal(provider)
	responses[statusTestAPI+"/namespaces/"+statusTestNS+"/providers/greeter"] = string(raw)
}

func statusUpdateObservation(t *testing.T, row destinationStatusAgent) *ReleaseRecordObservation {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Record *ReleaseRecordObservation `json:"releaseRecord"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields.Record
}

func TestDestinationStatusReleaseRecordObservation(t *testing.T) {
	for _, tc := range []struct {
		name              string
		version           int
		state, ready      string
		complete, pending bool
	}{
		{"v1 legacy missing", 1, "", "ready", true, false},
		{"v1 completed", 1, "completed", "ready", true, false},
		{"v2 completed", 2, "completed", "ready", true, false},
		{"v2 pending", 2, "pending", "ready", true, true},
		{"v2 partial", 2, "partial", "ready", true, false},
		{"v2 missing", 2, "unknown", "ready", false, false},
		{"v2 unreadable", 2, "unknown", "ready", false, false},
		{"v1 unreadable", 1, "unknown", "ready", false, false},
		{"v2 malformed", 2, "unknown", "ready", false, false},
		{"v2 oversized", 2, "unknown", "ready", false, false},
		{"future", 3, "unknown", "ready", false, false},
		{"target", 2, "unknown", "ready", false, false},
		{"record target", 2, "unknown", "ready", false, false},
		{"spec", 2, "unknown", "drifted", false, false},
		{"uid", 2, "unknown", "ready", false, false},
		{"generation", 2, "unknown", "pending", false, false},
		{"mixed", 2, "unknown", "ready", false, false},
		{"provider absent provenance", 2, "unknown", "ready", false, false},
		{"foreign provider", 2, "unknown", "drifted", false, false},
		{"root baseline", 2, "unknown", "ready", false, false},
		{"terminal mismatch", 2, "unknown", "ready", false, false},
		{"terminal attempt", 2, "unknown", "ready", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "no-private-history")
			t.Setenv("KMX_HOME", home)
			a, responses, out, _, _ := statusTestFixture(t)
			agent, provider, state := statusUpdatePair(t, tc.version)
			replace := func(doc map[string]any, edit func(*releaseLiftProvenance)) {
				var r releaseLiftProvenance
				ann := nativeMetadata(doc)["annotations"].(map[string]any)
				if json.Unmarshal([]byte(ann[releaseLiftRecordAnnotation].(string)), &r) != nil {
					t.Fatal("bad fixture")
				}
				edit(&r)
				statusTestReplaceRecord(t, doc, r)
			}
			switch tc.name {
			case "v2 pending":
				pending := state.History[0]
				pending.LiftID = strings.Repeat("e", 32)
				pending.State = "pending"
				state.Operation, state.Pending = pending.LiftID, &pending
			case "v2 partial":
				state.History[0].State = "partial"
			case "target":
				replace(agent, func(r *releaseLiftProvenance) { r.Target.NamespaceUID = "replacement" })
			case "record target":
				state.Target.ClusterUID = "other-cluster"
			case "spec":
				agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
			case "uid":
				nativeMetadata(agent)["uid"] = "replacement"
			case "generation":
				nativeMetadata(agent)["generation"] = 2
			case "mixed":
				replace(provider, func(r *releaseLiftProvenance) { r.LiftID = strings.Repeat("e", 32) })
			case "provider absent provenance":
				delete(nativeMetadata(provider)["annotations"].(map[string]any), releaseLiftRecordAnnotation)
			case "foreign provider":
				nativeMetadata(provider)["annotations"].(map[string]any)[orkaSuiteMarkers[4]] = strings.Repeat("7", 64)
			case "root baseline":
				state.Baseline[0].LiftRecordDigest = "sha256:" + strings.Repeat("7", 64)
			case "terminal mismatch":
				state.History[0].Objects[0].SpecDigest = "sha256:" + strings.Repeat("7", 64)
			case "terminal attempt":
				state.History[0].LiftID = strings.Repeat("e", 32)
			}
			wire := string(releaseStateWire(t, state))
			fail := strings.Contains(tc.name, "unreadable")
			switch tc.name {
			case "v1 legacy missing", "v2 missing":
				wire = ""
			case "v2 malformed":
				wire = `{"kind":"ConfigMap","private":"private-record-payload"}`
			case "v2 oversized":
				wire = strings.Repeat(" ", 128<<10) + "{}"
			}
			reads := statusUpdateRecordGET(t, a, &wire, &fail)
			statusUpdateInstall(t, responses, agent, provider)
			report, err := statusTestRun(t, a, out)
			if len(report.Agents) != 1 {
				t.Fatalf("lost row: %+v %v", report, err)
			}
			row := report.Agents[0]
			observation := statusUpdateObservation(t, row)
			if tc.state == "" {
				if observation != nil {
					t.Fatalf("legacy missing record enrolled: %+v", observation)
				}
			} else if observation == nil || observation.State != tc.state || observation.Name != releaseStateName("release-notes", "greeter", "orka") {
				t.Errorf("release state=%+v want=%s; provenance=%+v error=%v", observation, tc.state, row.Provenance, err)
			}
			if tc.state == "completed" && observation != nil && observation.UID != "record-uid" {
				t.Error("record UID lost")
			}
			if report.Complete != tc.complete || row.Readiness.State != tc.ready {
				t.Errorf("complete=%t readiness=%s want=%t/%s", report.Complete, row.Readiness.State, tc.complete, tc.ready)
			}
			if tc.pending && !errors.Is(err, ErrDestinationStatusPending) {
				t.Errorf("pending operation returned %v", err)
			}
			if (tc.state == "unknown" || tc.state == "partial") && (err == nil || errors.Is(err, ErrDestinationStatusPending)) {
				t.Errorf("unknown/partial returned %v", err)
			}
			if tc.state == "completed" && err != nil {
				t.Errorf("consistent completion failed: %v", err)
			}
			if *reads != 1 {
				t.Errorf("record GET count=%d want=1", *reads)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Errorf("status touched private state: %v", err)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "private-record-payload") || strings.Contains(string(raw), "tear down") {
				t.Error("unsafe payload or destructive guidance leaked")
			}
			out.Reset()
			_ = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
			table := out.String()
			if !strings.Contains(table, "RELEASE STATE") || !strings.Contains(table, "LIFT ATTEMPT") {
				t.Errorf("table lacks release/attempt identity: %s", table)
			}
			if tc.state != "" && !strings.Contains(table, tc.state) {
				t.Errorf("table disagrees with JSON state %s: %s", tc.state, table)
			}
			if tc.version <= 2 && tc.name != "target" && !strings.Contains(table, strings.Repeat("ab", 16)) {
				t.Error("table omits full fresh Lift attempt ID")
			}
			if strings.Contains(table, "private-record-payload") || strings.Contains(table, "tear down") {
				t.Error("unsafe record read guidance")
			}
		})
	}
}

func TestDestinationStatusPartialTableIdentifiesEachAttempt(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent, provider, state := statusUpdatePair(t, 1)
	var update releaseLiftProvenance
	ann := nativeMetadata(provider)["annotations"].(map[string]any)
	if err := json.Unmarshal([]byte(ann[releaseLiftRecordAnnotation].(string)), &update); err != nil {
		t.Fatal(err)
	}
	update.Version, update.LiftID = 2, strings.Repeat("ef", 16)
	statusTestReplaceRecord(t, provider, update)
	state.History[0].State = "partial"
	wire, fail := string(releaseStateWire(t, state)), false
	statusUpdateRecordGET(t, a, &wire, &fail)
	statusUpdateInstall(t, responses, agent, provider)
	_ = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "table"})
	if !strings.Contains(out.String(), "partial") || !strings.Contains(out.String(), "v1/"+strings.Repeat("ab", 16)) || !strings.Contains(out.String(), "v2/"+strings.Repeat("ef", 16)) {
		t.Fatalf("table hid the partial Provider mutation behind the Agent's old create: %s", out)
	}
}

func TestDestinationStatusReleaseRecordRechecksWholeFingerprint(t *testing.T) {
	for _, key := range []string{"kaimahi.dev/description", releaseCreateRecordAnnotation, "kaimahi.dev/bundle"} {
		t.Run(key, func(t *testing.T) {
			a, responses, out, _, _ := statusTestFixture(t)
			agent, provider, state := statusUpdatePair(t, 2)
			statusUpdateInstall(t, responses, agent, provider)
			wire, fail := string(releaseStateWire(t, state)), false
			statusUpdateRecordGET(t, a, &wire, &fail)
			// The Agent list held the original fingerprint. Change only the exact
			// reread, so checking the initial documents would incorrectly complete.
			nativeMetadata(agent)["annotations"].(map[string]any)[key] = "changed"
			raw, _ := json.Marshal(agent)
			responses[statusTestAPI+"/namespaces/"+statusTestNS+"/agents/greeter"] = string(raw)
			report, err := statusTestRun(t, a, out)
			observation := statusUpdateObservation(t, report.Agents[0])
			if err == nil || report.Complete || observation == nil || observation.State != "unknown" {
				t.Fatalf("stale %s fingerprint completed: %+v %v", key, observation, err)
			}
		})
	}
}

func TestDestinationStatusReleaseRecordReadFailureRetainsObservation(t *testing.T) {
	a, responses, out, _, _ := statusTestFixture(t)
	agent, provider, _ := statusUpdatePair(t, 2)
	statusUpdateInstall(t, responses, agent, provider)
	wire, fail := "", true
	statusUpdateRecordGET(t, a, &wire, &fail)
	_ = a.DestinationStatus(DestinationStatusOptions{Destination: "prod", Namespace: statusTestNS, Output: "json"})
	if !bytes.Contains(out.Bytes(), []byte(`"releaseRecord"`)) || bytes.Contains(out.Bytes(), []byte("private-record-payload")) {
		t.Fatal("read failure lost safe partial output", out.String())
	}
}
