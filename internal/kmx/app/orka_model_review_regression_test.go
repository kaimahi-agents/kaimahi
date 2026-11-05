package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOrkaModelLegacyAdoptionRefused(t *testing.T) {
	a, objects, roots := modelGatewayFixture(t)
	root, _ := modelGatewayRoot(roots)
	legacy := modelFixtureChildren(objects)[0]
	labels := map[string]any{}
	for k, v := range modelMap(modelMap(legacy["metadata"])["labels"]) {
		if k != "kaimahi.dev/model-operation" {
			labels[k] = v
		}
	}
	modelMap(legacy["metadata"])["labels"] = labels
	capture := a.captureCommand
	deletes := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		switch {
		case slices.Contains(cmd.Args, "delete"):
			deletes++
			fmt.Fprint(cmd.Stdout, `{"kind":"Status","apiVersion":"v1","status":"Success"}`)
			return nil
		case strings.Contains(args, "replicasets?limit=64"):
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"kind": "ReplicaSetList", "apiVersion": "apps/v1", "metadata": map[string]any{}, "items": []any{legacy}})
		case strings.Contains(args, "pods?limit=64"):
			fmt.Fprint(cmd.Stdout, `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[]}`)
			return nil
		}
		return capture(ctx, cmd)
	}
	expected := modelChild{"apps/v1", "ReplicaSet", modelText(modelMap(legacy["metadata"])["name"]), "owned-rs", root.UID}
	if err := a.modelDeleteOwned(context.Background(), OrkaNamespace, root, expected); err == nil || deletes != 0 {
		t.Fatalf("legacy adoption authorized deletion: err%v sends%d", err, deletes)
	}
}

func TestOrkaModelOperationSelector(t *testing.T) {
	first, _ := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "amd64")
	second, _ := modelDocuments("copilot", "gpt-4.1", "33333333-3333-4333-8333-333333333333", "amd64")
	one := modelMap(modelMap(first[1]["spec"])["selector"])["matchLabels"]
	two := modelMap(modelMap(second[1]["spec"])["selector"])["matchLabels"]
	rawOne, _ := json.Marshal(one)
	rawTwo, _ := json.Marshal(two)
	if string(rawOne) == string(rawTwo) {
		t.Fatal("ordinary orphan from another operation matches controller selector")
	}
}

func TestOrkaModelUnscheduledPod(t *testing.T) {
	a, objects, roots := modelGatewayFixture(t)
	root, _ := modelGatewayRoot(roots)
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		if strings.Contains(args, "pods?limit=64") || strings.HasSuffix(args, "/pods/copilot-vekil-pod") {
			for _, pod := range modelFixtureChildren(objects) {
				if pod["kind"] != "Pod" {
					continue
				}
				delete(modelMap(pod["spec"]), "nodeName")
				pod["status"] = map[string]any{"phase": "Pending"}
				if strings.Contains(args, "pods?limit=64") {
					return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"kind": "PodList", "apiVersion": "v1", "metadata": map[string]any{}, "items": []any{pod}})
				}
				return json.NewEncoder(cmd.Stdout).Encode(pod)
			}
		}
		return capture(ctx, cmd)
	}
	if kids, err := a.modelChildren(context.Background(), OrkaNamespace, root); err != nil || len(kids) != 2 {
		t.Fatalf("pending ownership must remain cleanable: %v %v", kids, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := a.modelServingGateway(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending Pod must wait, not fail placement or become ready: %v", err)
	}
}

func TestOrkaModelLocalProbeCancellation(t *testing.T) {
	a, _, _ := modelConfigureFixture(t)
	marker := filepath.Join(t.TempDir(), "sentinel")
	tool := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "kind")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf started > "+marker+"\nprintf 'test-control-plane\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = a.modelGatewayNodes(ctx)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled observation still started local inspection")
	}
}
