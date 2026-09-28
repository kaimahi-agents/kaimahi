package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Each row catches a plan that predicts an outcome different from the actual
// reconciliation, including a refusal whose attempted writes must remain zero.
func TestReconcilePlanAndDeployAgreeOnEveryOutcome(t *testing.T) {
	for _, tc := range []struct{ name, marker, change, want string }{
		{"created", "absent", "", "created"},
		{"reused", "own", "", "reused"},
		{"updated", "own", "drift", "updated"},
		{"adopted", "unmarked", "", "adopted"},
		{"refused unmarked drift", "unmarked", "drift", "refused"},
		{"refused foreign marker", "foreign", "", "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixture(t)
			if tc.marker != "absent" {
				provider := reconcileLive(t, dir, "Provider", rendered)
				seedReconcile(t, dir, provider)
				agent := reconcileLive(t, dir, "Agent", rendered)
				if tc.marker != "unmarked" {
					meta := agent["metadata"].(map[string]any)
					annotations, _ := meta["annotations"].(map[string]any)
					if annotations == nil {
						annotations = map[string]any{}
					}
					annotations[orkaBundleMarker] = map[string]string{"own": "sample", "foreign": "someone-else"}[tc.marker]
					annotations[orkaPortableMarker] = rendered.PortableDigest()
					annotations[orkaRenderedMarker] = rendered.RenderedDigest()
					meta["annotations"] = annotations
				}
				if tc.change == "drift" {
					agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "changed"}
				}
				seedReconcile(t, dir, agent)
			}
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			before := len(orkaCalls(t, dir))
			decisions, planErr := adapter.app.planOrkaReconcile(context.Background(), rendered, bundle, adapter.create.Namespace)
			for _, call := range orkaCalls(t, dir)[before:] {
				if call.Document != nil && !strings.Contains(strings.Join(call.Args, " "), "--dry-run=server") {
					t.Fatalf("plan wrote a resource: %+v", call)
				}
			}
			result, deployErr := adapter.Deploy(context.Background(), rendered, agentruntime.DeployOptions{Reconcile: true})
			if tc.want == "refused" {
				for _, call := range orkaCalls(t, dir)[before:] {
					if call.Document != nil && !strings.Contains(strings.Join(call.Args, " "), "--dry-run=server") {
						t.Fatalf("refused reconciliation wrote a resource: %+v", call)
					}
				}
				if planErr == nil || deployErr == nil {
					t.Fatalf("plan/deploy disagreement on refusal: %v / %v", planErr, deployErr)
				}
				if !strings.Contains(planErr.Error(), "Agent/sample") || !strings.Contains(deployErr.Error(), "Agent/sample") {
					t.Fatalf("unnamed refusal: %v / %v", planErr, deployErr)
				}
				return
			}
			if planErr != nil || deployErr != nil {
				t.Fatalf("plan/deploy: %v / %v", planErr, deployErr)
			}
			providerOutcome := agentruntime.ResourceAdopted
			if tc.marker == "absent" {
				providerOutcome = agentruntime.ResourceCreated
			}
			if len(decisions) != 2 || len(result.Receipt.Resources) != 2 || decisions[0].outcome != providerOutcome || result.Receipt.Resources[0].Outcome != providerOutcome || string(decisions[1].outcome) != tc.want || string(result.Receipt.Resources[1].Outcome) != tc.want {
				t.Fatalf("plan/deploy disagree: %+v / %+v, want %s", decisions, result.Receipt.Resources, tc.want)
			}
		})
	}
}

// A stale portable or rendered digest on an otherwise identical resource must
// plan as a reused outcome that still refreshes markers, and Deploy must
// perform exactly that write: only the marker annotations change, under the
// resource's current resourceVersion, and generation does not advance.
func TestReconcilePlanPredictsReusedMarkerRefresh(t *testing.T) {
	adapter, rendered, dir := reconcileFixture(t)
	if _, err := adapter.Deploy(context.Background(), rendered, agentruntime.DeployOptions{Reconcile: true}); err != nil {
		t.Fatal(err)
	}
	docs := []agentruntime.Document{agentruntime.ReviewDocument(rendered.Documents()[0])}
	for _, doc := range rendered.DeployDocuments() {
		docs = append(docs, agentruntime.ApplyDocument(doc))
	}
	revised, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("same authored spec with a comment"), docs)
	if err != nil {
		t.Fatal(err)
	}
	if revised.PortableDigest() == rendered.PortableDigest() {
		t.Fatal("fixture did not change the portable digest")
	}
	bundle, err := orkaBundleFromRendered(revised)
	if err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))
	decisions, err := adapter.app.planOrkaReconcile(context.Background(), revised, bundle, adapter.create.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if call.Document != nil && !strings.Contains(strings.Join(call.Args, " "), "--dry-run=server") {
			t.Fatalf("plan wrote a resource: %+v", call)
		}
	}
	if len(decisions) != 2 || decisions[1].outcome != agentruntime.ResourceReused || !decisions[1].markerRefresh {
		t.Fatalf("plan did not predict a reused marker refresh: %+v", decisions)
	}
	result, err := adapter.Deploy(context.Background(), revised, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Resources[1].Outcome != agentruntime.ResourceReused || result.Receipt.Resources[1].Generation != 1 {
		t.Fatalf("deploy disagreed with planned marker refresh: %+v", result.Receipt.Resources[1])
	}
	var sawMarkerRefreshWrite bool
	for _, call := range orkaCalls(t, dir)[before:] {
		if !slices.Contains(call.Args, "patch") || !slices.Contains(call.Args, "agents.core.orka.ai") {
			continue
		}
		if len(call.Patch) != 3 || call.Patch[0]["op"] != "test" || call.Patch[0]["path"] != "/metadata/resourceVersion" || call.Patch[0]["value"] != "1" {
			t.Fatalf("marker refresh patch missing resourceVersion precondition: %+v", call)
		}
		for _, op := range call.Patch[1:] {
			path, _ := op["path"].(string)
			if op["op"] != "replace" || !strings.HasPrefix(path, "/metadata/annotations/kaimahi.dev~1") {
				t.Fatalf("marker refresh patch touched unexpected field: %+v", call)
			}
		}
		sawMarkerRefreshWrite = true
	}
	if !sawMarkerRefreshWrite {
		t.Fatal("expected exactly one patch call refreshing the Agent markers")
	}
}
