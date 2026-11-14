package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaModelWireMetadata(t *testing.T) {
	a, _, roots := modelGatewayFixture(t)
	root, _ := modelGatewayRoot(roots)
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		if strings.Contains(args, "replicasets?limit=64") {
			fmt.Fprint(cmd.Stdout, `{"apiVersion":"apps/v1","kind":"ReplicaSetList","metadata":{"resourceVersion":"21"},"items":[]}`)
			return nil
		}
		if strings.HasSuffix(args, "/v1/models") {
			fmt.Fprint(cmd.Stdout, `{"object":"list","data":[{"id":"gpt-4.1","object":"model","created":0,"owned_by":"github-copilot","capabilities":{"type":"chat"}}]}`)
			return nil
		}
		return capture(ctx, cmd)
	}
	// Empty the Pod page too: only List metadata compatibility is under test here.
	childCapture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "/pods?limit=64") {
			fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"PodList","metadata":{"resourceVersion":"22"},"items":[]}`)
			return nil
		}
		return childCapture(ctx, cmd)
	}
	if _, err := a.modelChildren(context.Background(), OrkaNamespace, root); err != nil {
		t.Errorf("standard List metadata refused: %v", err)
	}
	if err := a.modelHealth(context.Background(), OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"}); err != nil {
		t.Errorf("OpenAI catalog metadata refused: %v", err)
	}
}

func TestOrkaModelMalformedCRI(t *testing.T) {
	// crictl v1.35 util.go uses protojson EmitDefaultValues: an empty list is [].
	for _, body := range []string{`{"containers":[null]}`, `{"containers":[{}]}`, `{"containers":[{"state":"CONTAINER_RUNNING","labels":{}}]}`, `{"containers":[{"state":"CONTAINER_RUNNING","labels":{"io.kubernetes.pod.uid":"other"}}]}`, `{"containers":null}`, `{"containers":[],"containers":[null]}`} {
		t.Run(body, func(t *testing.T) {
			a, _, roots := modelGatewayFixture(t)
			root, _ := modelGatewayRoot(roots)
			tool := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "docker")
			raw, err := os.ReadFile(tool)
			if err != nil {
				t.Fatal(err)
			}
			replacement := "printf '%s' '" + body + "'"
			script := strings.Replace(string(raw), `printf '{"containers":[]}'`, replacement, 1)
			if script == string(raw) {
				t.Fatal("fixture not replaced")
			}
			if err = os.WriteFile(tool, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err = a.modelGatewayStopped(context.Background(), root, nil); err == nil {
				t.Fatal("malformed process inventory proved stopped")
			}
		})
	}
}

func TestOrkaModelProfileAdmissionFields(t *testing.T) {
	docs, _ := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "amd64", "")
	raw, _ := json.Marshal(modelMap(modelMap(docs[1]["spec"])["template"])["spec"])
	var want, admitted map[string]any
	json.Unmarshal(raw, &want)
	json.Unmarshal(raw, &admitted)
	// Kubernetes v1.35 priority and DefaultTolerationSeconds admission defaults.
	admitted["priority"] = float64(0)
	admitted["preemptionPolicy"] = "PreemptLowerPriority"
	admitted["tolerations"] = []any{map[string]any{"key": "node.kubernetes.io/not-ready", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": float64(300)}, map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": float64(300)}}
	if !modelSpecMatches(want, admitted, ".template.spec", "Deployment") {
		t.Fatal("profile omitted standard admission fields")
	}
	admitted["priority"] = float64(1)
	if modelSpecMatches(want, admitted, ".template.spec", "Deployment") {
		t.Fatal("accepted changed priority")
	}
}

func TestOrkaModelChildSetDuplicates(t *testing.T) {
	a := modelChild{"v1", "Pod", "one", "one-uid", "rs-uid"}
	b := a
	b.Name, b.UID = "two", "two-uid"
	if sameModelChildren([]modelChild{a, a}, []modelChild{a, b}) {
		t.Fatal("duplicate identities hid an unretained child")
	}
}
