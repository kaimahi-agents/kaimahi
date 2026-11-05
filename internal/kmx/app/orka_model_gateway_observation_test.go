package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func modelGatewayFixture(t *testing.T) (*App, map[string]map[string]any, []modelOwnedResource) {
	t.Helper()
	a, objects, _ := modelConfigureFixture(t)
	docs, err := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	var roots []modelOwnedResource
	for _, doc := range docs {
		r, e := a.createModelObjectObserved(context.Background(), OrkaNamespace, doc)
		if e != nil {
			t.Fatal(e)
		}
		roots = append(roots, r)
	}
	return a, objects, roots
}

func TestOrkaModelGatewayObservation(t *testing.T) {
	for _, damage := range []string{"", "no-pod", "foreign-pod", "auth", "catalog", "terminating-Deployment", "terminating-Service", "terminating-Provider", "terminating-Secret"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, roots := modelGatewayFixture(t)
			if strings.HasPrefix(damage, "terminating-") {
				for _, obj := range objects {
					if obj["kind"] == strings.TrimPrefix(damage, "terminating-") {
						modelMap(obj["metadata"])["deletionTimestamp"] = "2026-11-05T05:30:00Z"
					}
				}
			}
			capture := a.captureCommand
			health := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if strings.Contains(args, "/proxy/") {
					health++
				}
				switch {
				case damage == "no-pod" && strings.Contains(args, "/pods?limit=64"):
					fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"PodList","metadata":{},"items":[]}`)
					return nil
				case damage == "foreign-pod" && strings.Contains(args, "/pods?limit=64"):
					for _, p := range modelFixtureChildren(objects) {
						if p["kind"] == "Pod" {
							modelMap(modelMap(p["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
							return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{}, "items": []any{p}})
						}
					}
				case damage == "auth" && strings.HasSuffix(args, "/readyz"):
					return fmt.Errorf("private-auth-body")
				case damage == "catalog" && strings.HasSuffix(args, "/v1/models"):
					fmt.Fprint(cmd.Stdout, `{"data":[{"id":"other"}]}`)
					return nil
				}
				return capture(ctx, cmd)
			}
			ctx := context.Background()
			if damage == "no-pod" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			children, err := a.modelCheckGateway(ctx, OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"}, roots)
			if damage == "" {
				if err != nil || len(children) != 2 {
					t.Fatalf("owned gateway %+v %v", children, err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("unsafe observation %s: %v", damage, err)
				}
				if damage == "no-pod" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("no Pod did not reach wait deadline: %v", err)
				}
				if (damage == "no-pod" || damage == "foreign-pod") && health != 0 {
					t.Fatal("health without owned serving Pod")
				}
			}
		})
	}
}

func TestOrkaModelGatewayStopObservation(t *testing.T) {
	for _, state := range []string{"absent", "unrelated", "CONTAINER_EXITED", "CONTAINER_RUNNING", "CONTAINER_UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			a, objects, roots := modelGatewayFixture(t)
			root, err := modelGatewayRoot(roots)
			if err != nil {
				t.Fatal(err)
			}
			kids, err := a.modelChildren(context.Background(), OrkaNamespace, root)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := modelPath(OrkaNamespace, root)
			delete(objects, path)
			gone, err := a.modelChildrenGone(context.Background(), OrkaNamespace, kids)
			if err != nil || !gone {
				t.Fatalf("child absence %v %v", gone, err)
			}
			if state != "absent" {
				t.Setenv("MODEL_CRI_RUNNING", "1")
				tool := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "docker")
				raw, e := os.ReadFile(tool)
				if e != nil {
					t.Fatal(e)
				}
				actual := state
				script := string(raw)
				if state == "unrelated" {
					actual = "CONTAINER_RUNNING"
					script = strings.ReplaceAll(script, "copilot-vekil-pod", "other.worker")
					script = strings.ReplaceAll(script, "owned-pod", "other-uid")
					script = strings.ReplaceAll(script, "orka-system", "other-system")
				}
				if e = os.WriteFile(tool, []byte(strings.ReplaceAll(script, "CONTAINER_RUNNING", actual)), 0700); e != nil {
					t.Fatal(e)
				}
			}
			err = a.modelGatewayStopped(context.Background(), root, kids)
			if (err == nil) != (state == "absent" || state == "unrelated" || state == "CONTAINER_EXITED") {
				t.Fatalf("false process stop %s: %v", state, err)
			}
		})
	}
}

func TestOrkaModelGatewayIdentitySet(t *testing.T) {
	a := modelChild{"v1", "Pod", "one", "one-uid", "rs-uid"}
	b := a
	b.Name, b.UID = "two", "two-uid"
	merged := mergeModelChildren([]modelChild{a}, []modelChild{a, b})
	if len(merged) != 2 || !sameModelChildren(merged, []modelChild{b, a}) || sameModelChildren(merged, []modelChild{a}) {
		t.Fatal("identity union lost or duplicated authority")
	}
	if _, err := modelGatewayRoot(nil); err == nil {
		t.Fatal("invented gateway")
	}
}

func TestOrkaModelCredentialMetadata(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			a, _, _ := modelConfigureFixture(t)
			if invalid {
				tool := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "stat")
				raw, err := os.ReadFile(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(tool, []byte(strings.ReplaceAll(string(raw), "regular file:0:65532:640:1:12", "regular file:0:65532:640:1:0")), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.modelCredentialInput(); (err != nil) != invalid {
				t.Fatalf("credential metadata %v: %v", invalid, err)
			}
		})
	}
}

// A child cannot retain cleanup authority by preserving its name after UID reuse.
func TestOrkaModelRetainedChildDrift(t *testing.T) {
	a, _, roots := modelGatewayFixture(t)
	root, _ := modelGatewayRoot(roots)
	kids, err := a.modelChildren(context.Background(), OrkaNamespace, root)
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "jsonpath={.metadata.uid}") {
			fmt.Fprint(cmd.Stdout, "reused-uid")
			return nil
		}
		return capture(ctx, cmd)
	}
	if _, err = a.modelChildrenGone(context.Background(), OrkaNamespace, kids); err == nil {
		t.Fatal("reused child accepted")
	}
}
