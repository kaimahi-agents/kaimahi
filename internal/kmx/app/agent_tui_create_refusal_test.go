package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Refusals consume no extra Task/write; these retain the real evaluated plan.
func TestConsoleCreationActualPlanRefusals(t *testing.T) {
	for _, stage := range []string{"digest", "cancelled", "source drift", "server drift", "live drift", "already applied"} {
		t.Run(stage, func(t *testing.T) {
			a, p, f := quickstartSuiteApplyFixture(t)
			review, err := a.reviewSuiteWorkspace(t.Context(), p.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			env := agentTUIEnvironment{Name: "kind-test", Local: true}
			server, err := a.consoleCreateTarget(t.Context(), env)
			if err != nil {
				t.Fatal(err)
			}
			request := consoleWorkspaceLiftRequest{env: env, server: server, namespace: OrkaNamespace, provider: "inference", review: review, prepared: p, reviewedDigest: p.Plan.Summary().PlanDigest}
			ctx := t.Context()
			switch stage {
			case "digest":
				request.reviewedDigest = "sha256:" + strings.Repeat("a", 64)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "source drift":
				writeFile(t, filepath.Join(p.Workspace, "eval/example.yaml"), "id: changed\ninput: changed\nexpectContains: [changed]\n")
			case "server drift":
				request.server = "other.example.invalid"
			case "live drift":
				doc := nativeOwnedDoc(t, p.Plan.native, "Agent")
				doc["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "operator edit"}
				seedReconcile(t, os.Getenv("KMX_RECONCILE_DIR"), doc)
			case "already applied":
				out := a.consoleWorkspaceLift(ctx, request)
				if out.err != nil || !out.verified {
					t.Fatalf("first apply: %v", out.err)
				}
			}
			beforeTasks, beforeWrites := f.taskCreates(t), len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR")))
			out := a.consoleWorkspaceLift(ctx, request)
			if out.err == nil || out.verified || out.result.State == "ready" {
				t.Fatalf("refusal claimed Ready: %+v", out)
			}
			if f.taskCreates(t) != beforeTasks || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != beforeWrites {
				t.Fatal("refused operation sent Tasks or placement writes")
			}
			if strings.Contains(fmt.Sprint(out.err), nativeCanary) {
				t.Fatal("error leaked payload")
			}
		})
	}
}
