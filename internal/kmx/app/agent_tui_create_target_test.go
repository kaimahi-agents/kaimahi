package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestConsoleCreationRejectsSameHostDifferentAPIPort(t *testing.T) {
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
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "config") {
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"contexts": []any{map[string]any{"name": "kind-test", "context": map[string]any{"cluster": "kind-test"}}}, "clusters": []any{map[string]any{"name": "kind-test", "cluster": map[string]any{"server": "https://127.0.0.1:8443"}}}})
		}
		return capture(ctx, cmd)
	}
	beforeTasks, beforeWrites := f.taskCreates(t), len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR")))
	out := a.consoleWorkspaceLift(t.Context(), consoleWorkspaceLiftRequest{env: env, server: server, namespace: OrkaNamespace, provider: "inference", review: review})
	if out.err == nil || out.prepared != nil || f.taskCreates(t) != beforeTasks || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != beforeWrites {
		t.Fatal("changed API port crossed execution consent")
	}
}
