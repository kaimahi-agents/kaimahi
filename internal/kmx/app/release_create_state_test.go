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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Only the external ConfigMap/SAR boundary is simulated. Pair placement still
// runs through the existing executable fixture and its independent write log.
type releaseStateAPIFixture struct {
	objects      map[string]map[string]any
	calls        []string
	writes       int
	fail         string
	loseFinalAck bool
}

func attachReleaseStateAPI(t *testing.T, a *App) *releaseStateAPIFixture {
	t.Helper()
	api := &releaseStateAPIFixture{objects: map[string]map[string]any{}}
	prior := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		verb, name := "", ""
		for _, v := range []string{"get", "patch"} {
			if i := slices.Index(args, v); i >= 0 && i+2 < len(args) && args[i+1] == "configmap" && strings.HasPrefix(args[i+2], "kmx-release-") {
				verb, name = v, args[i+2]
			}
		}
		if slices.Contains(args, "auth") && slices.Contains(args, "can-i") {
			for _, arg := range args {
				if arg == "configmaps" || strings.HasPrefix(arg, "configmaps/kmx-release-") {
					verb = "auth"
				}
			}
		}
		var doc map[string]any
		if slices.Contains(args, "-f") {
			raw, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = bytes.NewReader(raw)
			_ = json.Unmarshal(raw, &doc)
			if doc["kind"] == "ConfigMap" && strings.HasPrefix(orkaObjectName(doc), "kmx-release-") {
				verb, name = "create", orkaObjectName(doc)
			}
		}
		if verb == "" {
			return prior(ctx, cmd)
		}
		api.calls = append(api.calls, verb+" "+name)
		if api.fail == verb {
			return errors.New("record API unavailable")
		}
		if verb == "auth" {
			_, err := io.WriteString(cmd.Stdout, "yes\n")
			return err
		}
		live := api.objects[name]
		switch verb {
		case "get":
			if live == nil {
				return nil
			}
		case "create":
			if live != nil {
				return errors.New("already exists")
			}
			meta := doc["metadata"].(map[string]any)
			meta["uid"], meta["resourceVersion"] = "record-uid", "1"
			live, api.objects[name] = doc, doc
			api.writes++
		case "patch":
			if live == nil {
				return errors.New("not found")
			}
			raw, err := io.ReadAll(cmd.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			var ops []struct {
				Op, Path string
				Value    any
			}
			if json.Unmarshal(raw, &ops) != nil || len(ops) != 4 {
				t.Fatal("unexpected record patch shape")
			}
			meta, data := live["metadata"].(map[string]any), live["data"].(map[string]any)
			for i, path := range []string{"/metadata/uid", "/metadata/resourceVersion", "/data/release.json"} {
				actual := []any{meta["uid"], meta["resourceVersion"], data["release.json"]}[i]
				if ops[i].Op != "test" || ops[i].Path != path || !reflect.DeepEqual(ops[i].Value, actual) {
					return errors.New("conflict")
				}
			}
			if ops[3].Op != "replace" || ops[3].Path != "/data/release.json" {
				t.Fatal("unexpected record mutation")
			}
			data["release.json"] = ops[3].Value
			rv, _ := strconv.Atoi(meta["resourceVersion"].(string))
			meta["resourceVersion"] = strconv.Itoa(rv + 1)
			api.writes++
			var state struct {
				Operation string `json:"operation"`
			}
			_ = json.Unmarshal([]byte(data["release.json"].(string)), &state)
			if state.Operation == "" && api.loseFinalAck {
				return errors.New("lost final acknowledgment")
			}
		}
		return json.NewEncoder(cmd.Stdout).Encode(live)
	}
	return api
}

func nativePairCreate(cmd *exec.Cmd) bool {
	if !slices.Contains(cmd.Args, "create") || !slices.Contains(cmd.Args, "-f") {
		return false
	}
	raw, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		return false
	}
	cmd.Stdin = bytes.NewReader(raw)
	var doc map[string]any
	return json.Unmarshal(raw, &doc) == nil && (doc["kind"] == "Provider" || doc["kind"] == "Agent")
}

func TestReleaseCreateRetainsNewOwnershipBaseline(t *testing.T) {
	a, f, opt, _ := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	api := f.records
	if len(api.objects) != 1 || api.writes != 4 {
		t.Fatal("fresh pair did not reserve, checkpoint twice and finalize a shared record")
	}
	state, err := a.readReleaseState(t.Context(), releaseState{Suite: p.summary.Suite, Member: p.summary.Member, Runtime: "orka", Target: p.summary.Target})
	if err != nil || state == nil || state.State.Operation != "" || state.State.Pending != nil || len(state.State.Baseline) != 2 || len(state.State.History) != 1 {
		t.Fatal("successful pair has no complete released baseline")
	}
	for i, proof := range state.State.Baseline {
		if proof.UID != r.Resources[i].UID || proof.Generation != r.Resources[i].Generation {
			t.Fatal("baseline invented an acknowledged identity")
		}
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_HOME"), "release-lifts", r.LiftID, "3.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		UpdateProof struct {
			RecordUID string               `json:"recordUID"`
			Objects   []releaseStateObject `json:"objects"`
		} `json:"updateProof"`
	}
	if json.Unmarshal(raw, &record) != nil || record.UpdateProof.RecordUID != state.UID || !reflect.DeepEqual(record.UpdateProof.Objects, state.State.Baseline) {
		t.Fatal("new-version private proof did not bind the record UID and exact pair")
	}
}

func TestReleaseCreateRecordRefusalBeforePairEffects(t *testing.T) {
	for _, failure := range []string{"auth", "get", "create"} {
		t.Run(failure, func(t *testing.T) {
			a, f, opt, dir := releaseApplyFixture(t, "both")
			p, err := a.PrepareReleaseLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			f.records.fail = failure
			r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
			if err == nil || len(persistentOrkaCalls(t, dir)) != 0 || f.records.writes != 0 {
				t.Fatal("record refusal reached pair effects")
			}
			if (failure == "auth" || failure == "get") && (r.State != "not-applied" || !strings.Contains(err.Error(), "record")) {
				t.Fatal("preflight refusal lost its cause or implied placement effects")
			}
		})
	}
}

func TestReleaseCreatePartialKeepsReservation(t *testing.T) {
	a, f, opt, _ := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_RECONCILE_FAIL_ONCE", "Agent")
	r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err == nil || r.Resources[0].Outcome != "created" || r.Resources[1].Outcome != "unknown" {
		t.Fatal("partial placement was hidden")
	}
	state, err := a.readReleaseState(t.Context(), releaseState{Suite: p.summary.Suite, Member: p.summary.Member, Runtime: "orka", Target: p.summary.Target})
	if err != nil || state == nil || state.State.Operation != r.LiftID || state.State.Pending == nil || len(state.State.Baseline) != 0 {
		t.Fatal("incomplete first placement was enrolled or reservation lost")
	}
	if len(f.records.objects) != 1 {
		t.Fatal("partial placement record removed")
	}
}

func TestReleaseCreateOccupiedRecordDoesNotPrescribeTeardown(t *testing.T) {
	a, _, opt, _ := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	err = a.preflightReleaseStateCreate(t.Context(), releaseStateSubject(p.native.Subject))
	if err == nil || strings.Contains(err.Error(), "tear down") || !strings.Contains(err.Error(), "already exists") {
		t.Fatal("healthy occupied record prescribed destructive recovery", err)
	}
}

func TestReleaseCreateLostFinalAckCannotAuthorizeUpdate(t *testing.T) {
	a, reads, opt, _ := releaseApplyFixture(t, "both")
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	reads.records.loseFinalAck = true
	r, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if !errors.Is(err, errReleaseStateWrite) || r.State != "partial" {
		t.Fatal("lost final release acknowledgment reported success", err)
	}
	state, err := a.readReleaseState(t.Context(), releaseStateSubject(p.native.Subject))
	if err != nil || state == nil || state.State.Operation != "" {
		t.Fatal("fixture did not commit the unacknowledged release")
	}
	if _, err := readReleaseOwnership(t.Context(), r.LiftID); !errors.Is(err, errReleaseState) {
		t.Fatal("unacknowledged release became update authority", err)
	}
}

func TestWorkspaceLiftRecordPreflightBeforeSpend(t *testing.T) {
	a, opt, candidates, reads, _, writes := workspaceLiftFixture(t)
	reads.records.fail = "auth"
	p, err := PrepareWorkspaceLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, err := a.LiftWorkspace(t.Context(), p)
	if err == nil || r.Eval.Status != "not-attempted" || len(candidates.creates) != 0 || *writes != 0 || reads.records.writes != 0 {
		t.Fatal("record preflight was deferred until after eval/publication spend")
	}
}
