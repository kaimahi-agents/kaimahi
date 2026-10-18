package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
)

func releaseStateFixture() releaseState {
	digest := "sha256:" + strings.Repeat("1", 64)
	objects := []releaseStateObject{
		{Kind: "Provider", UID: "provider-uid", Generation: 1, SpecDigest: digest, OwnershipDigest: digest, LiftRecordDigest: digest},
		{Kind: "Agent", UID: "agent-uid", Generation: 1, SpecDigest: digest, OwnershipDigest: digest, LiftRecordDigest: digest},
	}
	return releaseState{Version: 1, Suite: "suite", Member: "greeter", Runtime: "orka",
		Target: ReleaseLiftTarget{"kind-test", "cluster-uid", OrkaNamespace, "namespace-uid"}, OriginLiftID: strings.Repeat("a", 32),
		Baseline: objects, History: []releaseStateRevision{{LiftID: strings.Repeat("a", 32), SuiteDigest: digest, EvidenceDigest: digest, State: "completed", Objects: objects}}}
}

func TestReleaseStateBoundsAndRollingHistory(t *testing.T) {
	s := releaseStateFixture()
	for i := 0; i < 12; i++ {
		r := s.History[0]
		r.LiftID = strings.Repeat("b", 30) + []string{"00", "01", "02", "03", "04", "05", "06", "07", "08", "09", "0a", "0b"}[i]
		s.History = append(s.History, r)
	}
	partial := s.History[0]
	partial.LiftID, partial.State = strings.Repeat("c", 32), "partial"
	s.History = append([]releaseStateRevision{partial}, s.History...)
	pending := s.History[len(s.History)-1]
	pending.LiftID, pending.State = strings.Repeat("d", 32), "pending"
	s.Operation, s.Pending = pending.LiftID, &pending
	raw, err := releaseStateBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	var got releaseState
	if err := agentsuite.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 11 || got.History[0].LiftID != partial.LiftID || got.History[1].LiftID != s.History[4].LiftID ||
		got.Pending == nil || got.Pending.LiftID != pending.LiftID || got.OriginLiftID != strings.Repeat("a", 32) {
		t.Fatal("rolling history lost partial, pending, origin or wrong completed entry")
	}
	if len(s.History) != 14 {
		t.Fatal("serialization mutated the caller's history")
	}
	for i := 0; i < 100; i++ {
		r := partial
		r.LiftID = strings.Repeat("e", 28) + strings.Repeat("0", 2) + string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16])
		s.History = append(s.History, r)
	}
	if _, err := releaseStateBytes(s); err == nil {
		t.Fatal("oversized partial history was pruned or accepted")
	}
}

func TestReleaseStateRejectsInvalidProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*releaseState)
	}{
		{"missing baseline", func(s *releaseState) { s.Baseline = nil }},
		{"lost operation", func(s *releaseState) { s.Operation = strings.Repeat("d", 32) }},
		{"foreign runtime", func(s *releaseState) { s.Runtime = "kagent" }},
		{"invalid target", func(s *releaseState) { s.Target.NamespaceUID = "" }},
		{"unknown UID", func(s *releaseState) { s.Baseline[0].UID = "" }},
		{"unknown generation", func(s *releaseState) { s.Baseline[0].Generation = 0 }},
		{"missing provenance", func(s *releaseState) { s.Baseline[0].LiftRecordDigest = "" }},
		{"invalid spec hash", func(s *releaseState) { s.Baseline[0].SpecDigest = "private-content-canary" }},
		{"duplicate object", func(s *releaseState) { s.Baseline[1].Kind = "Provider" }},
		{"duplicate attempt", func(s *releaseState) { s.History = append(s.History, s.History[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := releaseStateFixture()
			tc.mutate(&s)
			if _, err := releaseStateBytes(s); err == nil || strings.Contains(err.Error(), "private-content-canary") {
				t.Fatal("invalid state accepted or unsafe diagnostic")
			}
		})
	}
}

func TestReleaseStateNameIsStableAndBounded(t *testing.T) {
	name := releaseStateName("suite", "member", "orka")
	if name != releaseStateName("suite", "member", "orka") || len(name) > 63 ||
		name == releaseStateName("suite", "other", "orka") || name == releaseStateName("other", "member", "orka") ||
		len(releaseStateName(strings.Repeat("a", 253), strings.Repeat("b", 253), "orka")) > 63 {
		t.Fatal("release key is unstable, colliding or unbounded")
	}
}

func releaseStateWire(t *testing.T, s releaseState) []byte {
	t.Helper()
	raw, err := releaseStateBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": releaseStateName(s.Suite, s.Member, s.Runtime), "namespace": s.Target.Namespace,
			"uid": "record-uid", "resourceVersion": "10", "labels": map[string]any{"app.kubernetes.io/managed-by": "kmx", "kaimahi.dev/release-state": "v1"}},
		"data": map[string]any{"release.json": string(raw)}}
	wire, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestReleaseStateDecodeBindsRecordToTarget(t *testing.T) {
	s := releaseStateFixture()
	wire := releaseStateWire(t, s)
	snapshot, err := decodeReleaseState(wire, s)
	if err != nil || snapshot.UID != "record-uid" || snapshot.ResourceVersion != "10" {
		t.Fatal("valid record did not preserve conditional identity")
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"copied namespace", func(d map[string]any) { d["metadata"].(map[string]any)["namespace"] = "other" }},
		{"foreign marker", func(d map[string]any) { d["metadata"].(map[string]any)["labels"] = map[string]any{} }},
		{"terminating", func(d map[string]any) { d["metadata"].(map[string]any)["deletionTimestamp"] = "now" }},
		{"no UID", func(d map[string]any) { delete(d["metadata"].(map[string]any), "uid") }},
		{"no RV", func(d map[string]any) { delete(d["metadata"].(map[string]any), "resourceVersion") }},
		{"extra payload", func(d map[string]any) { d["data"].(map[string]any)["secret"] = "private-content-canary" }},
		{"unknown field", func(d map[string]any) {
			d["data"].(map[string]any)["release.json"] = strings.TrimSuffix(string(mustStateBytes(t, s)), "}") + `,"secret":"private-content-canary"}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(wire, &doc); err != nil {
				t.Fatal(err)
			}
			tc.mutate(doc)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeReleaseState(raw, s); err == nil || strings.Contains(err.Error(), "private-content-canary") {
				t.Fatal("foreign or ambiguous record accepted or diagnostic leaked payload")
			}
		})
	}
	for _, invalid := range []string{
		strings.Replace(string(wire), `"kind":"ConfigMap"`, `"kind":"Secret","kind":"ConfigMap"`, 1),
		strings.Replace(string(wire), `"kind":"ConfigMap"`, `"Kind":"ConfigMap"`, 1),
		strings.Replace(string(wire), `"uid":"record-uid"`, `"UID":"record-uid"`, 1),
	} {
		if _, err := decodeReleaseState([]byte(invalid), s); err == nil {
			t.Fatal("ambiguous or case-mismatched envelope was accepted")
		}
	}
	wrong := s
	wrong.Target.ClusterUID = "other-cluster"
	if _, err := decodeReleaseState(wire, wrong); err == nil {
		t.Fatal("copied cluster identity accepted")
	}
}

func TestReleaseStateCASBindsRecordAndOwner(t *testing.T) {
	initial := releaseStateFixture()
	old, err := decodeReleaseState(releaseStateWire(t, initial), initial)
	if err != nil {
		t.Fatal(err)
	}
	next := releaseStateFixture()
	pending := next.History[0]
	pending.LiftID, pending.State = strings.Repeat("b", 32), "pending"
	next.Operation, next.Pending = pending.LiftID, &pending
	a, _, _ := nativeFixture(t)
	calls := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		calls++
		if strings.Contains(strings.Join(cmd.Args, " "), "release.json") || !strings.Contains(strings.Join(cmd.Args, " "), "--patch-file=/dev/stdin") {
			t.Fatal("record contents entered argv or patch did not use stdin")
		}
		body, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		var ops []struct {
			Op, Path string
			Value    any
		}
		if err := json.Unmarshal(body, &ops); err != nil {
			t.Fatal(err)
		}
		if len(ops) != 4 || ops[0].Op != "test" || ops[0].Path != "/metadata/uid" || ops[0].Value != "record-uid" ||
			ops[1].Op != "test" || ops[1].Path != "/metadata/resourceVersion" || ops[1].Value != "10" ||
			ops[2].Op != "test" || ops[2].Path != "/data/release.json" || ops[2].Value != string(mustStateBytes(t, initial)) ||
			ops[3].Op != "replace" || ops[3].Path != "/data/release.json" {
			t.Fatal("CAS omitted reviewed UID, RV or complete record test")
		}
		if calls > 1 {
			_, _ = io.WriteString(cmd.Stderr, "private-content-canary conflict")
			return errors.New("conflict")
		}
		var submitted releaseState
		if err := agentsuite.Unmarshal([]byte(ops[3].Value.(string)), &submitted); err != nil || submitted.Operation != pending.LiftID {
			t.Fatal("CAS did not reserve the requested operation")
		}
		wire := strings.Replace(string(releaseStateWire(t, submitted)), `"resourceVersion":"10"`, `"resourceVersion":"11"`, 1)
		_, err = io.WriteString(cmd.Stdout, wire)
		return err
	}
	written, err := a.writeReleaseState(t.Context(), old, next, pending.LiftID)
	if err != nil || written.ResourceVersion != "11" || written.State.Operation != pending.LiftID {
		t.Fatal("acknowledged CAS lost reservation or fresh RV")
	}
	if _, err := a.writeReleaseState(t.Context(), old, next, pending.LiftID); err == nil || strings.Contains(err.Error(), "private-content-canary") || strings.Contains(err.Error(), "tear down") || calls != 2 {
		t.Fatal("conflict retried, succeeded, leaked content or presumed destructive recovery")
	}
	other := next
	other.Operation = strings.Repeat("c", 32)
	otherPending := pending
	otherPending.LiftID = other.Operation
	other.Pending = &otherPending
	if _, err := a.writeReleaseState(t.Context(), written, other, other.Operation); !errors.Is(err, errReleaseOperationInProgress) || calls != 2 {
		t.Fatal("another operation stole an in-flight reservation")
	}
	foreign := next
	foreign.Target.ClusterUID = "other"
	if _, err := a.writeReleaseState(t.Context(), old, foreign, pending.LiftID); err == nil || calls != 2 {
		t.Fatal("cross-target CAS reached API")
	}
	if !reflect.DeepEqual(old.State, initial) {
		t.Fatal("CAS mutated its reviewed baseline")
	}
}

func TestReleaseStateCreateAndReadFailClosed(t *testing.T) {
	s := releaseStateFixture()
	pending := s.History[0]
	pending.State = "pending"
	for i := range pending.Objects {
		pending.Objects[i].UID, pending.Objects[i].Generation = "", 0
	}
	s.Operation, s.Pending, s.Baseline, s.History = s.OriginLiftID, &pending, nil, nil
	a, _, _ := nativeFixture(t)
	calls := 0
	mode := "create"
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		calls++
		args := strings.Join(cmd.Args, " ")
		if !strings.Contains(args, "--context kind-test") || !strings.Contains(args, "-n orka-system") {
			t.Fatal("record request lost pinned context or namespace")
		}
		if mode == "fail" {
			_, _ = io.WriteString(cmd.Stderr, "private-content-canary forbidden")
			return errors.New("denied")
		}
		if mode == "absent" {
			return nil
		}
		if mode == "create" {
			if !strings.Contains(args, "create -f -") {
				t.Fatal("not create-only reservation")
			}
			body, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			var submitted map[string]any
			if err := json.Unmarshal(body, &submitted); err != nil {
				t.Fatal(err)
			}
			if submitted["kind"] != "ConfigMap" || submitted["metadata"].(map[string]any)["name"] != releaseStateName(s.Suite, s.Member, s.Runtime) ||
				submitted["data"].(map[string]any)["release.json"] != string(mustStateBytes(t, s)) {
				t.Fatal("reservation does not retain the frozen bounded intent")
			}
		} else if !strings.Contains(args, "get configmap ") || !strings.Contains(args, "--ignore-not-found") {
			t.Fatal("record inspection was not an exact GET")
		}
		_, err := cmd.Stdout.Write(releaseStateWire(t, s))
		return err
	}
	created, err := a.createReleaseState(t.Context(), s)
	if err != nil || created.State.Operation != s.OriginLiftID {
		t.Fatal("new intent not reserved")
	}
	mode = "read"
	read, err := a.readReleaseState(t.Context(), s)
	if err != nil || read.UID != created.UID {
		t.Fatal("record read lost identity")
	}
	mode = "absent"
	if read, err := a.readReleaseState(t.Context(), s); err != nil || read != nil {
		t.Fatal("absent record misclassified")
	}
	mode = "fail"
	before := calls
	if _, err := a.createReleaseState(t.Context(), s); err == nil || strings.Contains(err.Error(), "private-content-canary") || calls != before+1 {
		t.Fatal("failed create retried/adopted or leaked API payload")
	}
	if read, err := a.readReleaseState(t.Context(), s); err == nil || read != nil {
		t.Fatal("unreadable record treated as absent")
	}
	before = calls
	if _, err := a.createReleaseState(t.Context(), releaseStateFixture()); err == nil || calls != before {
		t.Fatal("new record adopted a completed baseline")
	}
}

func TestReleaseStatePreflightBeforePlacement(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "stale", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s := releaseStateFixture()
			a, _, _ := nativeFixture(t)
			calls := []string{}
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				calls = append(calls, args)
				if strings.Contains(args, " get configmap ") {
					if mode == "unknown" {
						return errors.New("read failed")
					}
					if mode == "stale" {
						_, err := cmd.Stdout.Write(releaseStateWire(t, s))
						return err
					}
					return nil
				}
				if !strings.Contains(args, " auth can-i ") {
					t.Fatal("preflight wrote or admitted")
				}
				answer := "yes\n"
				if mode == "deny" {
					answer = "no\n"
				}
				_, err := io.WriteString(cmd.Stdout, answer)
				return err
			}
			err := a.preflightReleaseStateCreate(t.Context(), s)
			if (err == nil) != (mode == "allow") {
				t.Fatal("record preflight did not fail closed")
			}
			if mode == "allow" {
				if len(calls) != 4 {
					t.Fatal("did not preflight get/create/patch")
				}
				for _, verb := range []string{"get", "patch"} {
					want := "auth can-i " + verb + " configmaps/" + releaseStateName(s.Suite, s.Member, s.Runtime)
					found := false
					for _, call := range calls {
						found = found || strings.Contains(call, want)
					}
					if !found {
						t.Fatal("preflight required broader than named-record RBAC")
					}
				}
			}
			if (mode == "unknown" || mode == "deny") && strings.Contains(err.Error(), "tear down") {
				t.Fatal("unverified permissions/connectivity recommended destructive recovery")
			}
			if (mode == "stale" || mode == "unknown") && len(calls) != 1 {
				t.Fatal("continued after unowned or unknown record")
			}
		})
	}
}

func mustStateBytes(t *testing.T, s releaseState) []byte {
	t.Helper()
	raw, err := releaseStateBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
