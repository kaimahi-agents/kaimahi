package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// This is a prerequisite characterization, not evidence-authorized Lift. It
// sends no Tasks and leaves its uniquely named fixtures for owned-cluster teardown.
func releaseUpdateLiveApp(t *testing.T) (*App, context.Context) {
	t.Helper()
	if os.Getenv("KMX_RELEASE_UPDATE_LIVE") != "1" {
		t.Skip("set KMX_RELEASE_UPDATE_LIVE=1 on the dedicated owned kind context")
	}
	contextName := os.Getenv("KMX_LIVE_KIND_CONTEXT")
	if contextName != "kind-kmx-update-proof" && contextName != "kind-kmx-orka" {
		t.Fatal("update qualification requires its dedicated or explicitly opted-in CI context")
	}
	t.Setenv("KMX_HOME", t.TempDir())
	a := &App{Cfg: &config.Config{KubeContext: contextName},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Out: io.Discard, Err: io.Discard}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	if err := requireLiveLocalKind(ctx, a); err != nil {
		t.Fatal("update qualification requires a verified loopback kind API")
	}
	if installed, err := a.orkaInstallState(); err != nil || !installed {
		t.Fatal("update qualification requires the exact kmx-pinned controller and chart")
	}
	return a, ctx
}

func TestReleaseUpdatePinnedRV(t *testing.T) {
	a, ctx := releaseUpdateLiveApp(t)
	kube := func(body any, args ...string) ([]byte, error) {
		var raw []byte
		var err error
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		return a.orkaCapture(ctx, raw, args...)
	}
	read := func(resource, name string) map[string]any {
		raw, err := kube(nil, "-n", OrkaNamespace, "get", resource, name, "-o", "json")
		var doc map[string]any
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			t.Fatalf("qualification read failed: %s", resource)
		}
		return doc
	}
	for _, name := range []string{"agents.core.orka.ai", "providers.core.orka.ai"} {
		crd := read("customresourcedefinitions", name)
		versions := crd["spec"].(map[string]any)["versions"].([]any)
		status := false
		for _, v := range versions {
			version := v.(map[string]any)
			if version["name"] == "v1alpha1" {
				subresources, _ := version["subresources"].(map[string]any)
				_, status = subresources["status"]
			}
		}
		if !status {
			t.Fatal("pinned CRD lacks the required status subresource")
		}
	}
	countTasks := func() int {
		raw, err := kube(nil, "-n", OrkaNamespace, "get", "tasks.core.orka.ai", "-o", "json")
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err != nil || json.Unmarshal(raw, &list) != nil {
			t.Fatal("cannot establish qualification Task inventory")
		}
		return len(list.Items)
	}
	tasksBefore := countTasks()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("cannot select a fresh qualification identity")
	}
	name := "kmx-update-proof-" + hex.EncodeToString(suffix[:])
	for _, resource := range []string{"agents.core.orka.ai", "providers.core.orka.ai", "secrets", "configmaps"} {
		raw, err := kube(nil, "-n", OrkaNamespace, "get", resource, name, "--ignore-not-found", "-o", "name")
		if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
			t.Fatal("qualification refuses existing or unreadable fixture objects; inspect owned cluster")
		}
	}
	create := func(doc map[string]any) {
		if _, err := kube(doc, "-n", OrkaNamespace, "create", "-f", "-", "-o", "name"); err != nil {
			t.Fatal("qualification create failed or ambiguous; inspect owned cluster, no automatic cleanup")
		}
	}
	// This synthetic key is local-only and never authorizes a hosted model. The
	// loopback endpoint is intentionally unusable; placement must not execute.
	create(map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name},
		"stringData": map[string]any{"api-key": "local-only"}})
	create(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name},
		"data": map[string]any{"operation": "qualification"}})
	reservation := read("configmaps", name)
	bundle, err := scaffold.GenerateOrka(scaffold.OrkaSpec{Name: name, Namespace: OrkaNamespace,
		ProviderType: "openai", Model: "local-proof", BaseURL: "http://127.0.0.1:1/v1",
		SecretName: name, Instructions: "Version one qualification fixture."})
	if err != nil {
		t.Fatal(err)
	}
	markers := map[string]any{orkaSuiteMarkers[0]: "update-proof", orkaSuiteMarkers[1]: "sha256:" + strings.Repeat("1", 64),
		orkaSuiteMarkers[2]: name, orkaSuiteMarkers[3]: "kmx.native-orka/v2", orkaSuiteMarkers[4]: strings.Repeat("2", 64),
		orkaSuiteMarkers[5]: strings.Repeat("3", 64)}
	for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
		addOrkaMarker(doc, markers)
		create(doc)
	}
	wait := func(doc map[string]any) {
		meta := doc["metadata"].(map[string]any)
		id := orkaIdentity{Kind: doc["kind"].(string), Name: name, UID: meta["uid"].(string), Generation: int64(meta["generation"].(float64))}
		if err := a.waitOrkaReady(ctx, OrkaNamespace, id); err != nil {
			t.Fatalf("pinned %s did not reach current-generation Ready; stop update work", id.Kind)
		}
	}
	provider, agent := read("providers.core.orka.ai", name), read("agents.core.orka.ai", name)
	wait(provider)
	wait(agent)
	provider, agent = read("providers.core.orka.ai", name), read("agents.core.orka.ai", name)
	meta := func(doc map[string]any) map[string]any { return doc["metadata"].(map[string]any) }
	testOp := func(path string, value any) map[string]any {
		return map[string]any{"op": "test", "path": path, "value": value}
	}
	patch := func(doc map[string]any, rv any, spec any, digest string) []map[string]any {
		return []map[string]any{
			testOp("/metadata/uid", meta(doc)["uid"]), testOp("/metadata/resourceVersion", rv),
			testOp("/metadata/generation", meta(doc)["generation"]), testOp("/spec", doc["spec"]),
			testOp("/metadata/annotations", meta(doc)["annotations"]),
			{"op": "replace", "path": "/spec", "value": spec},
			{"op": "replace", "path": "/metadata/annotations/kaimahi.dev~1suite-digest", "value": digest},
		}
	}
	applyPatch := func(resource string, ops []map[string]any, extra ...string) ([]byte, error) {
		args := []string{"-n", OrkaNamespace, "patch", resource, name, "--type=json", "--patch-file=/dev/stdin", "-o", "json"}
		return kube(ops, append(args, extra...)...)
	}
	// Induce deterministic status-only RV churn, instead of depending on a
	// controller's polling cadence. Never spoof Ready or its observed generation.
	statusPatch := []map[string]any{testOp("/metadata/resourceVersion", meta(agent)["resourceVersion"]),
		{"op": "add", "path": "/status/conditions/0/message", "value": "Resource-version qualification observation."}}
	if _, err := applyPatch("agents.core.orka.ai", statusPatch, "--subresource=status"); err != nil {
		t.Fatal("cannot induce status-only RV churn on the pinned CRD")
	}
	fresh := read("agents.core.orka.ai", name)
	if meta(fresh)["resourceVersion"] == meta(agent)["resourceVersion"] || meta(fresh)["generation"] != meta(agent)["generation"] ||
		meta(fresh)["uid"] != meta(agent)["uid"] || !reflect.DeepEqual(fresh["spec"], agent["spec"]) ||
		!reflect.DeepEqual(meta(fresh)["annotations"], meta(agent)["annotations"]) {
		t.Fatal("status-only churn changed authoritative state; stop update work")
	}
	v2, err := cloneOrkaDoc(agent["spec"].(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	v2["systemPrompt"] = map[string]any{"inline": "Version two qualification fixture."}
	newDigest := "sha256:" + strings.Repeat("4", 64)
	if _, err := applyPatch("agents.core.orka.ai", patch(agent, meta(agent)["resourceVersion"], v2, newDigest)); err == nil {
		t.Fatal("stale RV patch unexpectedly wrote")
	}
	// The Provider changes only version metadata: the inference binding is fixed.
	if _, err := applyPatch("providers.core.orka.ai", patch(provider, meta(provider)["resourceVersion"], provider["spec"], newDigest)); err != nil {
		t.Fatal("conditional Provider metadata update failed; stop update work")
	}
	wait(read("providers.core.orka.ai", name))
	currentReservation := read("configmaps", name)
	if meta(currentReservation)["uid"] != meta(reservation)["uid"] ||
		!reflect.DeepEqual(currentReservation["data"], reservation["data"]) {
		t.Fatal("reservation changed before Agent patch")
	}
	fresh = read("agents.core.orka.ai", name)
	if meta(fresh)["uid"] != meta(agent)["uid"] || meta(fresh)["generation"] != meta(agent)["generation"] ||
		!reflect.DeepEqual(fresh["spec"], agent["spec"]) || !reflect.DeepEqual(meta(fresh)["annotations"], meta(agent)["annotations"]) {
		t.Fatal("Agent authoritative state changed before its conditional patch")
	}
	ops := patch(agent, meta(fresh)["resourceVersion"], v2, newDigest)
	for _, extra := range [][]string{{"--dry-run=server"}, nil} {
		raw, err := applyPatch("agents.core.orka.ai", ops, extra...)
		var acknowledged map[string]any
		if err != nil || json.Unmarshal(raw, &acknowledged) != nil {
			t.Fatal("fresh-RV Agent patch failed; stop update work")
		}
		if meta(acknowledged)["uid"] != meta(agent)["uid"] || !reflect.DeepEqual(acknowledged["spec"], v2) {
			t.Fatal("pinned admission changed reviewed Agent state")
		}
		if len(extra) == 0 {
			wait(acknowledged)
		}
	}
	updated := read("agents.core.orka.ai", name)
	if meta(updated)["uid"] != meta(agent)["uid"] || meta(updated)["generation"] == meta(agent)["generation"] || !reflect.DeepEqual(updated["spec"], v2) {
		t.Fatal("pinned update lost identity or reviewed spec")
	}
	// Isolate each stale proof: all other tests and replacement values use the
	// current object, so changed annotations cannot mask a missing gen/spec test.
	for _, field := range []string{"generation", "spec"} {
		current := read("agents.core.orka.ai", name)
		ops := patch(current, meta(current)["resourceVersion"], current["spec"], newDigest)
		if field == "generation" {
			ops[2]["value"] = meta(agent)["generation"]
		} else {
			ops[3]["value"] = agent["spec"]
		}
		if _, err := applyPatch("agents.core.orka.ai", ops); err == nil {
			t.Fatalf("fresh RV bypassed the isolated %s test", field)
		}
		after := read("agents.core.orka.ai", name)
		if !reflect.DeepEqual(after, current) {
			t.Fatalf("isolated %s refusal changed the object or was masked by concurrent churn", field)
		}
	}
	if countTasks() != tasksBefore {
		t.Fatal("conditional placement qualification unexpectedly created Tasks")
	}
	t.Log("pinned controller: status-only RV churn, stale-RV refusal, fresh-RV update, generation/spec conflict and current-generation Ready; no Tasks")
}

func TestReleaseStatePinnedCAS(t *testing.T) {
	a, ctx := releaseUpdateLiveApp(t)
	s := releaseStateFixture()
	namespaceUID := func(name string) string {
		raw, err := a.orkaCapture(ctx, nil, "get", "namespace", name, "-o", "json")
		var object struct{ Metadata struct{ Name, UID string } }
		if err != nil || json.Unmarshal(raw, &object) != nil || object.Metadata.Name != name || object.Metadata.UID == "" {
			t.Fatal("cannot freeze record target identity")
		}
		return object.Metadata.UID
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("cannot select a fresh record name")
	}
	s.Member = "kmx-cas-proof-" + hex.EncodeToString(suffix[:])
	s.Target = ReleaseLiftTarget{a.Cfg.KubeContext, namespaceUID("kube-system"), OrkaNamespace, namespaceUID(OrkaNamespace)}
	intent := s
	pending := s.History[0]
	pending.Objects = append([]releaseStateObject(nil), pending.Objects...)
	pending.State = "pending"
	for i := range pending.Objects {
		pending.Objects[i].UID, pending.Objects[i].Generation = "", 0
	}
	intent.Operation, intent.Pending, intent.Baseline, intent.History = intent.OriginLiftID, &pending, nil, nil
	if err := a.preflightReleaseStateCreate(ctx, intent); err != nil {
		t.Fatal("record preflight failed")
	}
	created, err := a.createReleaseState(ctx, intent)
	if err != nil {
		t.Fatal("fresh create reservation failed")
	}
	if _, err := a.createReleaseState(ctx, intent); err == nil {
		t.Fatal("occupied create adopted a record")
	}
	ready, err := a.writeReleaseState(ctx, created, s, s.OriginLiftID)
	if err != nil {
		t.Fatal("cannot finalize API-protocol fixture baseline")
	}
	other := *a
	first, err := a.readReleaseState(ctx, s)
	if err != nil {
		t.Fatal("first client could not inspect baseline")
	}
	second, err := other.readReleaseState(ctx, s)
	if err != nil || second.ResourceVersion != first.ResourceVersion {
		t.Fatal("clients did not review the same state")
	}
	begin := func(id string) releaseState {
		next := ready.State
		p := next.History[0]
		p.LiftID, p.State, p.Before = id, "pending", next.Baseline
		next.Operation, next.Pending = id, &p
		return next
	}
	one, two := strings.Repeat("b", 32), strings.Repeat("c", 32)
	reserved, err := a.writeReleaseState(ctx, first, begin(one), one)
	if err != nil {
		t.Fatal("first client failed to reserve")
	}
	if _, err := other.writeReleaseState(ctx, second, begin(two), two); err == nil {
		t.Fatal("two clients reserved the same pair")
	}
	current, err := other.readReleaseState(ctx, s)
	if err != nil || current.State.Operation != one {
		t.Fatal("losing CAS changed the reservation")
	}
	if _, err := other.writeReleaseState(ctx, current, begin(two), two); !errors.Is(err, errReleaseOperationInProgress) {
		t.Fatal("fresh observer stole a held reservation")
	}
	wrongUID := *reserved
	wrongUID.UID = "replaced-record"
	if _, err := a.writeReleaseState(ctx, &wrongUID, reserved.State, one); err == nil {
		t.Fatal("record UID precondition was not enforced")
	}
	t.Log("real ConfigMap API: create-only reservation, UID/RV/data CAS, two independent clients and no takeover")
}
