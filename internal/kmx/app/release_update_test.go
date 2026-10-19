package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type updateAPIFixture struct {
	patches           int
	unreadyProvider   bool
	afterProvider     func()
	shared            bool
	sharedFallback    bool
	sharedUnqualified bool
	loseNativeAck     string
	mutateAdmission   bool
}

func releaseUpdateFixture(t *testing.T, configure ...func(*nativeReadFixture)) (*App, *nativeReadFixture, ReleaseLiftOptions, ReleaseLiftOptions, string, *updateAPIFixture) {
	t.Helper()
	toolPath := os.Getenv("PATH")
	_, _, next, _ := releaseLiftFixture(t, func(root string) {
		writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Changed version instructions.\n")
	})
	t.Setenv("PATH", toolPath)
	a, reads, first, dir := releaseApplyFixture(t, "both")
	for _, edit := range configure {
		edit(reads)
	}
	p, err := a.PrepareReleaseLift(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest); err != nil {
		t.Fatal(err)
	}
	if next.Reference == first.Reference {
		t.Fatal("fixture did not create two exact passing digests")
	}
	fixture := attachReleaseUpdateAPI(t, a, dir)
	return a, reads, first, next, dir, fixture
}

func attachReleaseUpdateAPI(t *testing.T, a *App, dir string) *updateAPIFixture {
	t.Helper()
	fixture := &updateAPIFixture{}
	prior := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args
		if slices.Contains(args, "auth") && slices.Contains(args, "can-i") {
			for _, arg := range args {
				if strings.HasPrefix(arg, "providers.core.orka.ai/") || strings.HasPrefix(arg, "agents.core.orka.ai/") {
					_, err := io.WriteString(cmd.Stdout, "yes\n")
					return err
				}
			}
		}
		if slices.Contains(args, "--raw") && strings.Contains(strings.Join(args, " "), "/agents?limit=256") {
			var doc map[string]any
			raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
			if err != nil || json.Unmarshal(raw, &doc) != nil {
				t.Fatal("missing live Agent fixture")
			}
			items := []any{doc}
			if fixture.shared || fixture.sharedFallback || fixture.sharedUnqualified {
				other, _ := cloneOrkaDoc(doc)
				nativeMetadata(other)["uid"], nativeMetadata(other)["name"], nativeMetadata(other)["namespace"] = "other-uid", "other-agent", "other-namespace"
				if fixture.sharedFallback {
					delete(other["spec"].(map[string]any), "providerRef")
					other["spec"].(map[string]any)["model"] = map[string]any{"fallbacks": []any{map[string]any{"providerRef": "greeter"}}}
				}
				if fixture.sharedUnqualified {
					delete(other["spec"].(map[string]any)["providerRef"].(map[string]any), "namespace")
				}
				items = append(items, other)
			}
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "AgentList", "metadata": map[string]any{}, "items": items})
		}
		i := slices.Index(args, "patch")
		if i < 0 || i+2 >= len(args) || (args[i+1] != "providers.core.orka.ai" && args[i+1] != "agents.core.orka.ai") {
			return prior(ctx, cmd)
		}
		path := filepath.Join(dir, args[i+1]+".json")
		raw, err := os.ReadFile(path)
		var doc map[string]any
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			t.Fatal("missing patch fixture")
		}
		body, _ := io.ReadAll(cmd.Stdin)
		var ops []struct {
			Op, Path string
			Value    any
		}
		if json.Unmarshal(body, &ops) != nil || len(ops) != 7 {
			t.Fatal("unexpected native conditional patch")
		}
		meta := nativeMetadata(doc)
		for j, key := range []string{"uid", "resourceVersion", "generation"} {
			if ops[j].Op != "test" || ops[j].Path != "/metadata/"+key || !reflect.DeepEqual(meta[key], ops[j].Value) {
				return errors.New("identity conflict")
			}
		}
		if ops[3].Op != "test" || ops[3].Path != "/spec" || !reflect.DeepEqual(doc["spec"], ops[3].Value) || ops[4].Op != "test" || ops[4].Path != "/metadata/annotations" || !reflect.DeepEqual(meta["annotations"], ops[4].Value) {
			return errors.New("spec/ownership conflict")
		}
		if ops[5].Op != "replace" || ops[5].Path != "/spec" || ops[6].Op != "replace" || ops[6].Path != "/metadata/annotations" {
			t.Fatal("unreviewed patch path")
		}
		if !reflect.DeepEqual(doc["spec"], ops[5].Value) {
			meta["generation"] = meta["generation"].(float64) + 1
		}
		doc["spec"], meta["annotations"] = ops[5].Value, ops[6].Value
		rv, _ := strconv.Atoi(meta["resourceVersion"].(string))
		meta["resourceVersion"] = strconv.Itoa(rv + 1)
		ready := !(fixture.unreadyProvider && args[i+1] == "providers.core.orka.ai")
		condition := "False"
		if ready {
			condition = "True"
		}
		doc["status"] = map[string]any{"ready": ready, "conditions": []any{map[string]any{"type": "Ready", "status": condition, "observedGeneration": meta["generation"]}}}
		if !slices.Contains(args, "--dry-run=server") {
			fixture.patches++
			if args[i+1] == "providers.core.orka.ai" {
				if fixture.unreadyProvider {
					if err := os.WriteFile(filepath.Join(dir, "provider-not-ready"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					_ = os.Remove(filepath.Join(dir, "provider-not-ready"))
				}
			}
			seedReconcile(t, dir, doc)
			if args[i+1] == "providers.core.orka.ai" && fixture.afterProvider != nil {
				fixture.afterProvider()
			}
		}
		if slices.Contains(args, "--dry-run=server") && fixture.mutateAdmission && args[i+1] == "agents.core.orka.ai" {
			doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "admission rewrite"}
		}
		if !slices.Contains(args, "--dry-run=server") && fixture.loseNativeAck == doc["kind"] {
			return errors.New("lost native acknowledgment")
		}
		return json.NewEncoder(cmd.Stdout).Encode(doc)
	}
	return fixture
}

func TestReleaseUpdateAndRollbackOriginalEvidence(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	for _, opt := range []ReleaseLiftOptions{next, old} {
		before := reads.records.writes
		p, err := a.prepareReleaseUpdate(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		if reads.records.writes != before {
			t.Fatal("update plan wrote")
		}
		r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
		if err != nil || r.State != "ready" || r.LiftID == "" {
			t.Fatalf("update/rollback not complete: %s %v", r.State, err)
		}
		if r.Resources[0].UID != p.before.State.Baseline[0].UID || r.Resources[1].UID != p.before.State.Baseline[1].UID {
			t.Fatal("update replaced UIDs")
		}
		if r.Summary.SuiteDigest != strings.Split(opt.Reference, "@")[1] {
			t.Fatal("wrong digest placed")
		}
	}
	if api.patches != 4 {
		t.Fatal("update/rollback retried or skipped a native object")
	}
	p, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	before := reads.records.writes
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err != nil || reads.records.writes != before || api.patches != 4 {
		t.Fatal("same-digest update repaired or rewrote")
	}
}

func TestReleaseUpdateFinalReadFailureCannotReturnAuthority(t *testing.T) {
	for _, failure := range []string{"pair", "reservation"} {
		t.Run(failure, func(t *testing.T) {
			a, _, _, next, _, api := releaseUpdateFixture(t)
			p, err := a.prepareReleaseUpdate(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			prior := a.captureCommand
			verified, failed := false, false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := cmd.Args
				if verified && !failed && slices.Contains(args, "get") && ((failure == "pair" && slices.Contains(args, "providers.core.orka.ai") && slices.Contains(args, "greeter")) || (failure == "reservation" && slices.Contains(args, "configmap"))) {
					failed = true
					return errors.New("final inspection read unavailable")
				}
				err := prior(ctx, cmd)
				if err == nil && slices.Contains(args, "agents.core.orka.ai") && slices.Contains(args, "--ignore-not-found=true") {
					verified = true
				}
				return err
			}
			r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
			if !failed || api.patches != 2 {
				t.Fatal("finalization failure was not isolated", err)
			}
			if err == nil || r.State == "ready" || r.placement != nil || r.ReleaseRecord.State != "unknown" {
				t.Fatal("unacknowledged finalization returned placement authority", r.State, err)
			}
			state, err := a.readReleaseState(t.Context(), p.before.State)
			if err != nil || state.State.Operation == "" {
				t.Fatal("unknown final observation released the reservation", err)
			}
		})
	}
}

func TestReleaseUpdateRefusesDriftOrSharing(t *testing.T) {
	for _, change := range []string{"spec", "uid", "metadata", "provenance", "shared", "fallback shared", "unqualified shared", "missing-private-proof"} {
		t.Run(change, func(t *testing.T) {
			a, _, _, next, dir, api := releaseUpdateFixture(t)
			path := filepath.Join(dir, "agents.core.orka.ai.json")
			raw, _ := os.ReadFile(path)
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			switch change {
			case "spec":
				doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
			case "uid":
				nativeMetadata(doc)["uid"] = "replacement"
			case "metadata":
				nativeMetadata(doc)["annotations"].(map[string]any)["kaimahi.dev/description"] = "drift"
			case "provenance":
				nativeMetadata(doc)["annotations"].(map[string]any)[releaseLiftRecordAnnotation] = "{}"
			case "shared":
				api.shared = true
			case "fallback shared":
				api.sharedFallback = true
			case "unqualified shared":
				api.sharedUnqualified = true
			case "missing-private-proof":
				t.Setenv("KMX_HOME", privateSuiteDir(t))
			}
			seedReconcile(t, dir, doc)
			if _, err := a.prepareReleaseUpdate(t.Context(), next); err == nil || api.patches != 0 {
				t.Fatal("unsafe update was eligible")
			}
		})
	}
}

func TestReleaseUpdateKnownPartialCanRollbackUnhealthy(t *testing.T) {
	a, _, old, next, _, api := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	api.unreadyProvider = true
	// Deterministically expire only the reached readiness phase, not parent
	// cancellation/preflight. The parent remains live for known-state recording.
	a.waitTiming.timeout = func(parent context.Context, phase string, d time.Duration) (context.Context, context.CancelFunc) {
		if phase == "orka-ready-provider" {
			return context.WithTimeout(parent, 0)
		}
		if d == 0 {
			return parent, func() {}
		}
		return context.WithTimeout(parent, d)
	}
	r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if err == nil || r.Resources[0].Outcome != "updated" || r.Resources[1].Outcome != "not-attempted" {
		t.Fatalf("partial facts lost: state=%s outcomes=%+v err=%v patches=%d", r.State, r.Resources, err, api.patches)
	}
	api.unreadyProvider = false
	a.waitTiming.timeout = nil
	rollback, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil {
		t.Fatal("known unhealthy partial cannot rollback", err)
	}
	if result, err := a.applyReleaseUpdate(t.Context(), rollback, rollback.lift.summary.PlanDigest); err != nil || result.State != "ready" {
		t.Fatal("rollback required reevaluation or prior Ready", err)
	}
}

func TestReleaseUpdateReservesCompletionCapacityBeforeEffects(t *testing.T) {
	s := releaseStateFixture()
	r := s.History[0]
	r.LiftID, r.State = strings.Repeat("b", 32), "pending"
	r.Before = append([]releaseStateObject(nil), s.Baseline...)
	r.Objects = append([]releaseStateObject(nil), r.Objects...)
	for i := range r.Objects {
		r.Objects[i].UID = ""
		r.Objects[i].Generation = 0
	}
	s.Operation, s.Pending = r.LiftID, &r
	if err := reserveReleaseUpdateCapacity(s); err != nil {
		t.Fatal("small operation refused", err)
	}
	for i := 0; i < 100; i++ {
		raw, err := releaseStateBytes(s)
		if err != nil {
			t.Fatal(err)
		}
		padding := maxReleaseStateBytes - 64 - len(raw)
		if padding >= 0 && padding < 900 {
			s.Target.Context += strings.Repeat("x", padding)
			if _, err := releaseStateBytes(s); err != nil {
				t.Fatal("pending fixture must still fit", err)
			}
			if reserveReleaseUpdateCapacity(s) == nil {
				t.Fatal("pending fit concealed unreserved checkpoint/completion growth")
			}
			return
		}
		partial := s.History[0]
		partial.LiftID, partial.State = fmt.Sprintf("%032x", i+1), "partial"
		s.History = append(s.History, partial)
	}
	t.Fatal("did not reach a bounded capacity edge")
}

func TestReleaseUpdateRecordPermissionBeforeEffects(t *testing.T) {
	a, reads, _, next, _, api := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	before := reads.records.writes
	reads.records.fail = "auth"
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); !errors.Is(err, errReleaseStateRBAC) || reads.records.writes != before || api.patches != 0 {
		t.Fatal("record permission checked after effects", err)
	}
}

func TestReleaseUpdateAmbiguousNativeWriteStaysBlocked(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	api.loseNativeAck = "Agent"
	r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if err == nil || r.State != "partial" || r.Resources[1].Outcome != "unknown" || api.patches != 2 {
		t.Fatal("unknown send retried or invented acknowledgment", err)
	}
	before := reads.records.writes
	if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseOperationInProgress) || reads.records.writes != before {
		t.Fatal("unknown send became recovery authority", err)
	}
}

func TestReleaseUpdateAdmissionRewriteRefusesBeforeNativeEffects(t *testing.T) {
	a, reads, old, next, _, api := releaseUpdateFixture(t)
	api.mutateAdmission = true
	before := reads.records.writes
	for i := 0; i < 5; i++ {
		p, err := a.prepareReleaseUpdate(t.Context(), next)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err == nil || api.patches != 0 || reads.records.writes != before {
			t.Fatal("admission refusal wrote or accumulated non-rollable history", err)
		}
	}
	p, err := a.prepareReleaseUpdate(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err != nil || api.patches != 0 || reads.records.writes != before {
		t.Fatal("no-effect refusals invalidated same-digest no-write", err)
	}
}

func TestReleaseUpdatePreSendRefusalRestoresKnownRecord(t *testing.T) {
	for _, failure := range []string{"transient inventory", "pre-send cancel"} {
		t.Run(failure, func(t *testing.T) {
			a, _, old, next, _, api := releaseUpdateFixture(t)
			p, err := a.prepareReleaseUpdate(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prior := a.captureCommand
			reserved, failed := false, false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := cmd.Args
				if reserved && !failed && ((failure == "transient inventory" && slices.Contains(args, "--raw") && strings.Contains(strings.Join(args, " "), "/agents?limit=256")) || (failure == "pre-send cancel" && slices.Contains(args, "get") && slices.Contains(args, "configmap"))) {
					failed = true
					if failure == "pre-send cancel" {
						cancel()
						return context.Canceled
					}
					return errors.New("transient list failure")
				}
				err := prior(ctx, cmd)
				if err == nil && slices.Contains(args, "patch") && slices.Contains(args, "configmap") {
					reserved = true
				}
				return err
			}
			if _, err := a.applyReleaseUpdate(ctx, p, p.lift.summary.PlanDigest); err == nil || !failed || api.patches != 0 {
				t.Fatal("pre-send failure was not exercised", err)
			}
			state, err := a.readReleaseState(t.Context(), p.before.State)
			if err != nil || state == nil || state.State.Operation != "" || state.data != p.before.data {
				t.Fatal("known zero-effect refusal stranded or advanced the release", err)
			}
			retry, err := a.prepareReleaseUpdate(t.Context(), old)
			if err != nil {
				t.Fatal("healthy pair now requires destructive recovery", err)
			}
			if _, err := a.applyReleaseUpdate(t.Context(), retry, retry.lift.summary.PlanDigest); err != nil || api.patches != 0 {
				t.Fatal("old no-write operation stopped being a no-op", err)
			}
		})
	}
}

func TestReleaseUpdateGenerationRefreshRefuses(t *testing.T) {
	a, reads, _, next, dir, api := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents.core.orka.ai.json")
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	nativeMetadata(doc)["generation"] = float64(2)
	seedReconcile(t, dir, doc)
	before := reads.records.writes
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); !errors.Is(err, errReleaseUpdateDrift) || reads.records.writes != before || api.patches != 0 {
		t.Fatal("fresh RV blessed a new generation", err)
	}
}

func TestReleaseUpdateLostFinalAckBlocksNextAttempt(t *testing.T) {
	a, reads, old, next, _, _ := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	reads.records.loseFinalAck = true
	r, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest)
	if !errors.Is(err, errReleaseStateWrite) || r.State != "partial" {
		t.Fatal("lost terminal acknowledgment reported success", err)
	}
	writes := reads.records.writes
	if _, err := a.prepareReleaseUpdate(t.Context(), old); !errors.Is(err, errReleaseState) || reads.records.writes != writes {
		t.Fatal("unknown final checkpoint authorized rollback", err)
	}
}

func TestReleaseUpdateRefusesConcurrentActor(t *testing.T) {
	a, _, _, next, _, api := releaseUpdateFixture(t)
	one, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	blocked := false
	other := *a
	otherHome := privateSuiteDir(t)
	api.afterProvider = func() {
		api.afterProvider = nil
		originalHome := os.Getenv("KMX_HOME")
		t.Setenv("KMX_HOME", otherHome)
		_, err := other.applyReleaseUpdate(t.Context(), two, two.lift.summary.PlanDigest)
		t.Setenv("KMX_HOME", originalHome)
		blocked = errors.Is(err, errReleaseOperationInProgress)
	}
	if _, err := a.applyReleaseUpdate(t.Context(), one, one.lift.summary.PlanDigest); err != nil || !blocked || api.patches != 2 {
		t.Fatal("pair-wide reservation failed", err)
	}
}

func TestReleaseUpdateStatusChurnDoesNotRefreshAuthority(t *testing.T) {
	a, _, _, next, dir, _ := releaseUpdateFixture(t)
	p, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Provider", "Agent"} {
		path := filepath.Join(dir, orkaPlural(kind)+".json")
		raw, _ := os.ReadFile(path)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		nativeMetadata(doc)["resourceVersion"] = "90"
		nativeMetadata(doc)["annotations"].(map[string]any)["example.invalid/independent"] = "preserve"
		seedReconcile(t, dir, doc)
	}
	if _, err := a.applyReleaseUpdate(t.Context(), p, p.lift.summary.PlanDigest); err != nil {
		t.Fatal("status-only RV churn refused", err)
	}
	// Mutation observations deliberately exceed the shipped reader's v1 contract.
	p2, err := a.prepareReleaseUpdate(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	doc := p2.current[1]
	ann := nativeMetadata(doc)["annotations"].(map[string]any)
	if ann["example.invalid/independent"] != "preserve" {
		t.Fatal("update overwrote independent metadata")
	}
	if frozenV081StatusRecord(ann[releaseLiftRecordAnnotation].(string), "orka") != nil {
		t.Fatal("v0.8.1 reader credited mutation provenance")
	}
	if !json.Valid([]byte(ann[releaseCreateRecordAnnotation].(string))) {
		t.Fatal("original create observation lost")
	}
}
