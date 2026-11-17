package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestOrkaModelReadRequiresAcknowledgment(t *testing.T) {
	expected := modelOwnedResource{APIVersion: "v1", Kind: "Service", Name: "model", Marker: "11111111-1111-4111-8111-111111111111", Purpose: "connection", Spec: map[string]any{}}
	for _, missing := range []string{"uid", "rv"} {
		want := expected
		if missing == "uid" {
			want.ResourceVersion = "7"
		} else {
			want.UID = "owned-uid"
		}
		a := New(&config.Config{})
		calls := 0
		a.captureCommand = func(context.Context, *exec.Cmd) error {
			calls++
			return fmt.Errorf("must not inspect unacknowledged resource")
		}
		if _, _, err := a.readModelObject(context.Background(), OrkaNamespace, want); err == nil || calls != 0 {
			t.Fatalf("missing %s sent%d calls: %v", missing, calls, err)
		}
	}
}

func TestOrkaModelSecretRejectsWriteOverrides(t *testing.T) {
	for _, field := range []string{"stringData", "unexpected"} {
		a := New(&config.Config{})
		calls := 0
		a.captureCommand = func(context.Context, *exec.Cmd) error { calls++; return fmt.Errorf("must not send Secret override") }
		doc := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "annotations": map[string]any{modelOperationAnnotation: "11111111-1111-4111-8111-111111111111"}}, "type": "Opaque", "data": map[string]any{"api-key": "bm90LXVzZWQtYnktdGhpcy1lbmRwb2ludA=="}, field: map[string]any{"api-key": "synthetic-override"}}
		if _, err := a.createModelObjectObserved(context.Background(), OrkaNamespace, doc); err == nil || calls != 0 {
			t.Fatalf("field%s sends%d error%v", field, calls, err)
		}
	}
}

func TestOrkaModelRead(t *testing.T) {
	for _, api := range []struct{ kind, version, presence, snapshot string }{
		{"ReplicaSet", "apps/v1", "replicasets.apps", "replicasets.v1.apps"},
		{"Pod", "v1", "pods", "pods"},
		{"Service", "v1", "services", "services"},
		{"Secret", "v1", "secrets", "secrets"},
		{"Deployment", "apps/v1", "deployments.apps", "deployments.v1.apps"},
		{"Provider", "core.orka.ai/v1alpha1", "providers.core.orka.ai", "providers.v1alpha1.core.orka.ai"},
		{"Agent", "core.orka.ai/v1alpha1", "agents.core.orka.ai", "agents.v1alpha1.core.orka.ai"},
		{"Task", "core.orka.ai/v1alpha1", "tasks.core.orka.ai", "tasks.v1alpha1.core.orka.ai"},
	} {
		generational := slices.Contains([]string{"Deployment", "Provider", "Agent", "Task"}, api.kind)
		cases := []string{"present", "absent-first", "absent-second", "wrong-first-uid", "error-first", "cancel-first", "error-second", "cancel-second", "whitespace", "malformed", "null", "list", "kind", "version", "name", "namespace", "uid", "owner"}
		if generational {
			cases = append(cases, "nondeleting", "null-timestamp", "invalid-timestamp", "missing-grace", "nonzero-grace", "negative-grace", "string-grace", "generation-jump", "invalid-generation", "fractional-generation", "spec", "already-terminating", "overflow", "same-generation", "same-generation-normal")
		}
		for _, damage := range cases {
			t.Run(api.kind+"/"+damage, func(t *testing.T) {
				want := modelOwnedResource{APIVersion: api.version, Kind: api.kind, Name: "model", UID: "owned", ResourceVersion: "7", Marker: "11111111-1111-4111-8111-111111111111", Purpose: "connection", Generation: 1, Spec: map[string]any{}}
				meta := map[string]any{"name": want.Name, "namespace": OrkaNamespace, "uid": want.UID, "resourceVersion": "8", "generation": float64(1), "annotations": map[string]any{modelOperationAnnotation: want.Marker}, "ownerReferences": []any{map[string]any{"uid": "owner"}}}
				obj := map[string]any{"apiVersion": api.version, "kind": api.kind, "metadata": meta, "spec": want.Spec}
				if api.kind == "Secret" {
					want.Spec = map[string]any{"type": "Opaque", "data": map[string]any{}}
					obj["type"], obj["data"] = want.Spec["type"], want.Spec["data"]
					delete(obj, "spec")
				}
				if generational {
					meta["generation"], meta["deletionTimestamp"], meta["deletionGracePeriodSeconds"] = float64(2), "2026-11-16T22:36:05Z", float64(0)
					meta["finalizers"] = []any{"foregroundDeletion"}
				}
				switch damage {
				case "nondeleting", "missing-grace":
					delete(meta, map[string]string{"nondeleting": "deletionTimestamp", "missing-grace": "deletionGracePeriodSeconds"}[damage])
				case "null-timestamp", "invalid-timestamp":
					meta["deletionTimestamp"] = map[string]any{"null-timestamp": nil, "invalid-timestamp": "invalid"}[damage]
				case "nonzero-grace", "negative-grace", "string-grace":
					meta["deletionGracePeriodSeconds"] = map[string]any{"nonzero-grace": float64(30), "negative-grace": float64(-1), "string-grace": "0"}[damage]
				case "generation-jump", "invalid-generation", "fractional-generation", "same-generation", "same-generation-normal":
					meta["generation"] = map[string]any{"generation-jump": float64(3), "invalid-generation": float64(0), "fractional-generation": 2.5, "same-generation": float64(1), "same-generation-normal": float64(1)}[damage]
					if damage == "same-generation-normal" {
						delete(meta, "deletionTimestamp")
						delete(meta, "deletionGracePeriodSeconds")
					}
				case "spec":
					obj["spec"] = map[string]any{"private-payload": "https://private.invalid/Bearer-secret"}
				case "already-terminating":
					want.Terminating = true
				case "overflow":
					want.Generation, meta["generation"] = math.MaxInt64, float64(math.MinInt64)
				}
				a := New(&config.Config{})
				calls := 0
				a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
					calls++
					if slices.Contains(cmd.Args, "delete") {
						t.Fatal("native generation granted new DELETE authority")
					}
					presence := slices.Contains(cmd.Args, "jsonpath={.metadata.uid}")
					resource, stage := api.snapshot, "second"
					if presence {
						resource, stage = api.presence, "first"
					}
					get := slices.Index(cmd.Args, "get")
					if get < 0 || presence != (calls%2 == 1) || cmd.Args[get+1] != resource || cmd.Args[get+2] != "model" || !slices.Contains(cmd.Args, OrkaNamespace) || !slices.Contains(cmd.Args, "--ignore-not-found=true") {
						t.Fatalf("unqualified %s read: %v", stage, cmd.Args)
					}
					switch damage {
					case "absent-" + stage:
						return nil
					case "error-" + stage:
						return fmt.Errorf("private read denial")
					case "cancel-" + stage:
						return context.Canceled
					}
					if presence {
						uid := "owned"
						if damage == "wrong-first-uid" {
							uid = "reused"
						}
						_, err := fmt.Fprint(cmd.Stdout, uid)
						return err
					}
					if raw, ok := map[string]string{"whitespace": " \n\t", "malformed": "{", "null": "null", "list": "[]"}[damage]; ok {
						_, err := fmt.Fprint(cmd.Stdout, raw)
						return err
					}
					switch damage {
					case "kind", "version":
						obj[map[string]string{"kind": "kind", "version": "apiVersion"}[damage]] = "other"
					case "name", "namespace", "uid":
						meta[damage] = "other"
					case "owner":
						meta["ownerReferences"] = []any{map[string]any{"uid": "foreign"}}
						meta["annotations"] = map[string]any{modelOperationAnnotation: "foreign"}
					}
					return json.NewEncoder(cmd.Stdout).Encode(obj)
				}
				var gone bool
				var err error
				if api.kind == "ReplicaSet" || api.kind == "Pod" {
					gone, err = a.modelChildrenGone(context.Background(), OrkaNamespace, []modelChild{{APIVersion: api.version, Kind: api.kind, Name: want.Name, UID: want.UID, OwnerUID: "owner"}})
				} else {
					var got modelOwnedResource
					var present bool
					got, present, err = a.readModelObject(context.Background(), OrkaNamespace, want)
					gone = !present
					if err == nil && present && (got.ResourceVersion != "8" || got.UID != want.UID || got.Marker != want.Marker || !reflect.DeepEqual(got.Spec, want.Spec) || generational && (got.Generation != int64(meta["generation"].(float64)) || got.Terminating != (damage != "same-generation-normal"))) {
						t.Fatalf("fresh/native observation lost: %+v", got)
					}
				}
				presentCase := damage == "present" || strings.HasPrefix(damage, "same-generation")
				refused := !presentCase && damage != "absent-first" && damage != "absent-second"
				if (err != nil) != refused || !refused && gone == presentCase {
					t.Fatalf("gone%t err%v", gone, err)
				}
				expected := 2
				if slices.Contains([]string{"absent-first", "wrong-first-uid", "error-first", "cancel-first"}, damage) {
					expected = 1
				}
				if calls != expected {
					t.Fatalf("reads%d want%d", calls, expected)
				}
				if generational && damage == "present" {
					if err := a.modelDeleteOwned(context.Background(), OrkaNamespace, want); err != nil {
						t.Fatal(err)
					}
				}
				reason := map[string]string{"kind": "model_resource_identity_invalid", "uid": "model_resource_ownership_changed", "wrong-first-uid": "model_resource_ownership_changed", "invalid-generation": "model_resource_generation_invalid", "spec": "model_resource_spec_changed", "generation-jump": "model_resource_generation_changed"}[damage]
				if generational && reason != "" {
					for _, cause := range []error{err, fmt.Errorf("private-url https://private.invalid/Bearer-secret: %w", err)} {
						modelRequireObservationReason(t, cause, reason)
					}
					for _, override := range []error{context.Canceled, context.DeadlineExceeded} {
						if modelFailure("execution_unknown", errors.Join(err, override)).(*OrkaModelError).DiagnosticReason() != "" {
							t.Fatal("reason survived cancellation/deadline")
						}
					}
				}
			})
		}
	}
}

func TestOrkaModelCapture(t *testing.T) {
	for _, kind := range []string{"Service", "Secret", "Deployment", "Provider", "Agent", "Task"} {
		t.Run(kind, func(t *testing.T) {
			desired := map[string]any{"apiVersion": map[string]string{"Service": "v1", "Secret": "v1", "Deployment": "apps/v1", "Provider": "core.orka.ai/v1alpha1", "Agent": "core.orka.ai/v1alpha1", "Task": "core.orka.ai/v1alpha1"}[kind], "kind": kind, "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "annotations": map[string]any{modelOperationAnnotation: "11111111-1111-4111-8111-111111111111"}}, "spec": map[string]any{}}
			if kind == "Secret" {
				delete(desired, "spec")
				desired["type"] = "Opaque"
				desired["data"] = map[string]any{"api-key": "bm90LXVzZWQtYnktdGhpcy1lbmRwb2ludA=="}
			}
			for _, damage := range []string{"", "uid", "rv", "marker", "kind", "namespace", "spec", "terminating", "defaults"} {
				t.Run(damage, func(t *testing.T) {
					a := New(&config.Config{KubeContext: "kind-test"})
					calls := 0
					a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
						calls++
						raw, _ := io.ReadAll(cmd.Stdin)
						var obj map[string]any
						if err := json.Unmarshal(raw, &obj); err != nil {
							return err
						}
						meta := obj["metadata"].(map[string]any)
						meta["uid"] = "owned-uid"
						meta["resourceVersion"] = "7"
						if kind == "Deployment" || kind == "Provider" || kind == "Agent" || kind == "Task" {
							meta["generation"] = float64(1)
						}
						switch damage {
						case "terminating":
							meta["deletionTimestamp"] = "2026-11-05T00:00:00Z"
						case "defaults":
							if kind == "Deployment" {
								obj["spec"].(map[string]any)["revisionHistoryLimit"] = float64(10)
							}
						case "uid":
							delete(meta, "uid")
						case "rv":
							delete(meta, "resourceVersion")
						case "marker":
							meta["annotations"] = map[string]any{}
						case "kind":
							obj["kind"] = "Pod"
						case "namespace":
							meta["namespace"] = "other"
						case "spec":
							if kind == "Secret" {
								obj["data"] = map[string]any{"api-key": "unknown"}
							} else {
								obj["spec"] = map[string]any{"injected": "unexpected"}
							}
						}
						return json.NewEncoder(cmd.Stdout).Encode(obj)
					}
					got, err := a.createModelObjectObserved(context.Background(), OrkaNamespace, desired)
					if damage == "" || damage == "defaults" {
						if err != nil {
							t.Fatal(err)
						}
						if got.UID != "owned-uid" {
							t.Fatal(got)
						}
						if err := validateModelResource(got, true); err != nil {
							t.Fatal(err)
						}
						if kind == "Agent" || kind == "Task" {
							cp := modelTestCheckpoint()
							cp.Resources = []modelOwnedResource{got}
							if err := validateModelCheckpoint(cp); err != nil {
								t.Fatal(err)
							}
							raw, err := encodeModelState(cp)
							if err != nil {
								t.Fatal(err)
							}
							var reopened modelCheckpoint
							if err := decodeModelState(bytes.NewReader(raw), &reopened); err != nil {
								t.Fatal(err)
							}
							if err := validateModelCheckpoint(reopened); err != nil {
								t.Fatal(err)
							}
						}
					} else if err == nil {
						t.Fatalf("accepted changed %s", damage)
					}
					if calls != 1 {
						t.Fatalf("replayed create: %d", calls)
					}
				})
			}
		})
	}
	t.Run("unknown GVK zero sends", func(t *testing.T) {
		a := New(&config.Config{})
		a.captureCommand = func(context.Context, *exec.Cmd) error { t.Fatal("sent unsupported object"); return nil }
		_, err := a.createModelObjectObserved(context.Background(), OrkaNamespace, map[string]any{"apiVersion": "evil/v1", "kind": "Service"})
		if err == nil {
			t.Fatal("accepted unknown kind")
		}
	})
	t.Run("lost acknowledgment never read or resent", func(t *testing.T) {
		a := New(&config.Config{})
		calls := 0
		a.captureCommand = func(context.Context, *exec.Cmd) error { calls++; return fmt.Errorf("lost") }
		doc := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "annotations": map[string]any{modelOperationAnnotation: "11111111-1111-4111-8111-111111111111"}}, "spec": map[string]any{}}
		_, err := a.createModelObjectObserved(context.Background(), OrkaNamespace, doc)
		if err == nil || calls != 1 {
			t.Fatalf("err %v calls %d", err, calls)
		}
		if strings.Contains(err.Error(), "lost") {
			t.Fatal("raw error surfaced")
		}
	})
}
