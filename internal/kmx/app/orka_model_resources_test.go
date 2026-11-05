package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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

func TestOrkaModelReadQualifiedResource(t *testing.T) {
	for kind, qualified := range map[string]string{"Deployment": "deployments.apps", "Provider": "providers.core.orka.ai", "Agent": "agents.core.orka.ai", "Task": "tasks.core.orka.ai"} {
		t.Run(kind, func(t *testing.T) {
			api, _ := modelAPIFor(kind)
			purpose := "connection"
			if kind == "Agent" || kind == "Task" {
				purpose = "proof"
			}
			want := modelOwnedResource{APIVersion: api.version, Kind: kind, Name: "model", UID: "owned-uid", ResourceVersion: "7", Marker: "11111111-1111-4111-8111-111111111111", Purpose: purpose, Generation: 1, Spec: map[string]any{}}
			a := New(&config.Config{})
			calls := 0
			a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
				calls++
				if calls == 1 {
					for _, arg := range cmd.Args {
						if arg == qualified {
							fmt.Fprint(cmd.Stdout, "owned-uid")
							return nil
						}
					}
					return nil
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": api.version, "kind": kind, "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "uid": "owned-uid", "resourceVersion": "8", "generation": 1, "annotations": map[string]any{modelOperationAnnotation: want.Marker}}, "spec": map[string]any{}})
			}
			if _, exists, err := a.readModelObject(context.Background(), OrkaNamespace, want); err != nil || !exists || calls != 2 {
				t.Fatalf("qualified%s calls%d exists%v err%v", qualified, calls, exists, err)
			}
		})
	}
}

func TestOrkaModelRead(t *testing.T) {
	expected := modelOwnedResource{APIVersion: "v1", Kind: "Service", Name: "model", UID: "owned-uid", ResourceVersion: "7", Marker: "11111111-1111-4111-8111-111111111111", Purpose: "connection", Spec: map[string]any{}}
	for _, absent := range []bool{false, true} {
		a := New(&config.Config{})
		calls := 0
		a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
			calls++
			if calls == 1 {
				if !absent {
					fmt.Fprint(cmd.Stdout, "owned-uid")
				}
				return nil
			}
			return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "uid": "owned-uid", "resourceVersion": "8", "annotations": map[string]any{modelOperationAnnotation: expected.Marker}}, "spec": map[string]any{}})
		}
		got, exists, err := a.readModelObject(context.Background(), OrkaNamespace, expected)
		if err != nil || exists == absent {
			t.Fatalf("read: %v %v", exists, err)
		}
		if exists && got.ResourceVersion != "8" {
			t.Fatal("not fresh RV")
		}
		if absent && calls != 1 {
			t.Fatal("absence triggered extra query")
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
