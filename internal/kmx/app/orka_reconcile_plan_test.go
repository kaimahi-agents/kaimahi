package app

import (
	"context"
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
