package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Consumes two independently completed real passing evaluations and a fresh
// public-Lift baseline on the dedicated owned source cluster. It does not
// manufacture/relabel evidence, evaluate, provision a model or send any Tasks.
func TestReleaseUpdateLiveOriginalEvidenceRoundTrip(t *testing.T) {
	home := os.Getenv("KMX_LIVE_UPDATE_HOME")
	if home == "" {
		t.Skip("requires the private home/layouts/evidence from an owned native qualification")
	}
	a, ctx := releaseUpdateLiveApp(t)
	t.Setenv("KMX_HOME", home)
	a.Cfg.ContextSource, a.Cfg.Confirm = config.SourceFlag, a.Cfg.KubeContext
	options := []ReleaseLiftOptions{}
	for _, version := range []string{"V1", "V2"} {
		layout, evidence := os.Getenv("KMX_LIVE_UPDATE_"+version+"_LAYOUT"), os.Getenv("KMX_LIVE_UPDATE_"+version+"_EVIDENCE")
		var index struct {
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
		}
		raw, err := os.ReadFile(layout + "/index.json")
		if err != nil || json.Unmarshal(raw, &index) != nil || len(index.Manifests) != 1 || evidence == "" {
			t.Fatal("requires one exact layout and original passing evidence per version")
		}
		options = append(options, ReleaseLiftOptions{Reference: "suite@" + index.Manifests[0].Digest, FromLayout: layout, Evidence: evidence, ToContext: a.Cfg.KubeContext, ToNamespace: OrkaNamespace, Inference: "provider:native-suite-inference", Promote: true})
	}
	if options[0].Reference == options[1].Reference {
		t.Fatal("qualification requires two different passing digests")
	}
	count := func() int {
		raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "tasks.core.orka.ai", "-o", "json")
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if err != nil || json.Unmarshal(raw, &list) != nil {
			t.Fatal("cannot establish Task count")
		}
		return len(list.Items)
	}
	before := count()
	var original []releaseStateObject
	for _, opt := range []ReleaseLiftOptions{options[1], options[0]} {
		p, err := a.prepareReleaseUpdate(ctx, opt)
		if err != nil {
			t.Fatal("real update preparation refused", err)
		}
		if original == nil {
			original = append([]releaseStateObject(nil), p.before.State.Baseline...)
		}
		r, err := a.applyReleaseUpdate(ctx, p, p.lift.summary.PlanDigest)
		if err != nil || r.State != "ready" || r.LiftID == "" {
			t.Fatal("real update/rollback incomplete", err)
		}
		for i, row := range r.Resources {
			if row.UID != original[i].UID || !row.Ready || row.Outcome != "updated" {
				t.Fatal("real update replaced UID or failed current-generation readiness")
			}
		}
		state, err := a.readReleaseState(ctx, p.before.State)
		if err != nil || state.State.Operation != "" || state.State.History[len(state.State.History)-1].SuiteDigest != strings.Split(opt.Reference, "@")[1] {
			t.Fatal("real release completion not recorded")
		}
		proof, err := readReleaseOwnership(ctx, r.LiftID)
		if err != nil || !releaseOwnershipMatches(proof, state) {
			t.Fatal("real private completion acknowledgment missing", err)
		}
	}
	// Force a second status-only dependency RV change specifically before the
	// same-digest no-op. Its owned markers must remain exactly as acknowledged.
	raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "providers.core.orka.ai", "native-suite-inference", "-o", "json")
	var inference map[string]any
	if err != nil || json.Unmarshal(raw, &inference) != nil {
		t.Fatal("no-op RV fixture read refused", err)
	}
	meta := inference["metadata"].(map[string]any)
	patch := []map[string]any{}
	for _, key := range []string{"uid", "resourceVersion", "generation"} {
		patch = append(patch, map[string]any{"op": "test", "path": "/metadata/" + key, "value": meta[key]})
	}
	patch = append(patch, map[string]any{"op": "test", "path": "/spec", "value": inference["spec"]}, map[string]any{"op": "add", "path": "/status/message", "value": "Owned no-op RV qualification"})
	body, _ := json.Marshal(patch)
	raw, err = a.orkaCapture(ctx, body, "-n", OrkaNamespace, "patch", "providers.core.orka.ai", "native-suite-inference", "--subresource=status", "--type=json", "--patch-file=/dev/stdin", "-o", "json")
	var refreshed map[string]any
	if err != nil || json.Unmarshal(raw, &refreshed) != nil || refreshed["metadata"].(map[string]any)["resourceVersion"] == meta["resourceVersion"] {
		t.Fatal("no-op RV churn not exercised", err)
	}
	p, err := a.prepareReleaseUpdate(ctx, options[0])
	if err != nil {
		t.Fatal(err)
	}
	if !p.noop {
		t.Fatal("rollback did not invalidate the later digest baseline")
	}
	r, err := a.applyReleaseUpdate(ctx, p, p.lift.summary.PlanDigest)
	if err != nil || r.State != "ready" || r.LiftID != "" {
		t.Fatal("same-digest real no-op after RV churn refused", err)
	}
	if err := a.VerifyReleaseLiftPlacement(ctx, r); err != nil {
		t.Fatal("real retained placement proof unusable after no-op", err)
	}
	state, err := a.readReleaseState(ctx, p.before.State)
	if err != nil || state.ResourceVersion != p.before.ResourceVersion || state.data != p.before.data {
		t.Fatal("real no-op changed shared record", err)
	}
	if count() != before {
		t.Fatal("update or original-evidence rollback sent a Task")
	}
	t.Log("real v1 -> v2 -> original-evidence v1 preserves both UIDs, records each completion and sends zero Tasks")
}
