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

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func modelGatewayFixture(t *testing.T, criBody ...string) (*App, map[string]map[string]any, []modelOwnedResource) {
	t.Helper()
	a, objects, _ := modelConfigureFixture(t, criBody...)
	docs, err := modelDocuments("copilot", "gpt-4.1", "11111111-1111-4111-8111-111111111111", "amd64", "")
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
	for _, damage := range []string{"", "no-pod", "foreign-pod", "auth", "catalog", "terminating-Deployment", "terminating-Service", "terminating-Provider", "terminating-Secret", "ownership-serving", "ownership-final-check"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, roots := modelGatewayFixture(t)
			root, _ := modelGatewayRoot(roots)
			if strings.HasPrefix(damage, "terminating-") {
				for _, obj := range objects {
					if obj["kind"] == strings.TrimPrefix(damage, "terminating-") {
						modelMap(obj["metadata"])["deletionTimestamp"] = "2026-11-05T05:30:00Z"
					}
				}
			}
			capture := a.captureCommand
			health, fullAfterChange := 0, 0
			catalogSeen, changed := false, false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if changed && (strings.Contains(args, "--raw") || slices.Contains(cmd.Args, "--ignore-not-found=true") && slices.Contains(cmd.Args, "json")) {
					fullAfterChange++
				}
				catalogSeen = catalogSeen || strings.HasSuffix(args, "/v1/models")
				if strings.HasPrefix(damage, "ownership-") && slices.Contains(cmd.Args, "jsonpath={.metadata.uid}") && (damage != "ownership-final-check" || catalogSeen) {
					changed = true
					fmt.Fprint(cmd.Stdout, "replaced-observation-uid")
					return nil
				}
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
			var children []modelChild
			var err error
			if damage == "ownership-serving" {
				_, err = a.modelServingGateway(ctx, root)
			} else {
				children, err = a.modelCheckGateway(ctx, OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"}, roots)
			}
			if strings.HasPrefix(damage, "ownership-") {
				if !changed || fullAfterChange != 0 || damage == "ownership-final-check" && !catalogSeen {
					t.Fatalf("wrong boundary: changed%t full%d catalog%t", changed, fullAfterChange, catalogSeen)
				}
				modelRequireObservationReason(t, err, "model_resource_ownership_changed")
			}
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
	uid := strings.Repeat("a1", 16)
	if verificationIdentity(uid) || secretshapes.Match(uid) == nil {
		t.Fatal("global credential/identity policy was weakened")
	}
	for _, tc := range []struct {
		name, uid, namespace, pod, state, refusal string
		retained                                  bool
	}{
		{"unrelated static Pod", uid, "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "", false},
		{"retained static UID", uid, "kube-system", "unrelated", "CONTAINER_RUNNING", "model credential process stop is unknown", true},
		{"gateway prefix", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_RUNNING", "model credential process stop is unknown", false},
		{"owned created", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_CREATED", "model credential process stop is unknown", false},
		{"owned unknown", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_UNKNOWN", "model credential process stop is unknown", false},
		{"owned exited", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_EXITED", "", false},
		{"unrelated invalid state", uid, "kube-system", "etcd-test-control-plane", "INVALID", "model process state unavailable", false},
		{"empty UID", "", "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"control UID", "bad\nuid", "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"long UID", strings.Repeat("x", 257), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"key shaped UID", strings.Join([]string{"sk", "proj", strings.Repeat("X", 100)}, "-"), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"uppercase hash", strings.Repeat("AB", 16), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"bad namespace", uid, "bad.namespace", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"bad name", uid, "kube-system", "INVALID", "CONTAINER_RUNNING", "model process identity unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"containers": []any{map[string]any{"state": tc.state, "labels": map[string]any{"io.kubernetes.pod.uid": tc.uid, "io.kubernetes.pod.namespace": tc.namespace, "io.kubernetes.pod.name": tc.pod}}}})
			a, objects, roots := modelGatewayFixture(t, string(body))
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
			if tc.retained {
				kids = append(kids, modelChild{Kind: "Pod", UID: uid})
			}
			err = a.modelGatewayStopped(context.Background(), root, kids)
			if tc.refusal == "" && err != nil || tc.refusal != "" && (err == nil || err.Error() != tc.refusal) {
				t.Fatalf("process refusal=%v want %q", err, tc.refusal)
			}
		})
	}
}

func modelRequireObservationReason(t *testing.T, err error, reason string) {
	t.Helper()
	safe := modelFailure("execution_unknown", err)
	var typed *OrkaModelError
	if !errors.As(safe, &typed) || typed.Code() != "execution_unknown" || typed.DiagnosticReason() != reason || typed.Error() != "The execution outcome is unknown." {
		t.Fatalf("reason %s lost: %v (%v)", reason, err, safe)
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
	for _, route := range []string{"copilot", "key"} {
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
				if err := a.modelCredentialInput(route); (err != nil) != invalid {
					t.Fatalf("credential metadata %v: %v", invalid, err)
				}
			})
		}
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
