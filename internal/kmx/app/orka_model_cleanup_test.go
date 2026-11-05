package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestOrkaModelDelete(t *testing.T) {
	root := modelOwnedResource{APIVersion: "v1", Kind: "Service", Name: "model", UID: "owned", ResourceVersion: "1", Marker: "11111111-1111-4111-8111-111111111111", Purpose: "connection", Spec: map[string]any{}}
	for _, damage := range []string{"", "uid", "marker", "lost", "terminating"} {
		t.Run(damage, func(t *testing.T) {
			a := New(&config.Config{})
			deletes := 0
			a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if strings.Contains(args, "delete --raw") {
					deletes++
					raw, _ := io.ReadAll(cmd.Stdin)
					var body map[string]any
					json.Unmarshal(raw, &body)
					if body["propagationPolicy"] != "Foreground" {
						t.Fatal("not foreground")
					}
					pre := modelMap(body["preconditions"])
					if pre["uid"] != "owned" || pre["resourceVersion"] != "9" {
						t.Fatalf("stale delete: %s", raw)
					}
					if damage == "lost" {
						return fmt.Errorf("lost private-body")
					}
					fmt.Fprint(cmd.Stdout, `{"kind":"Status","apiVersion":"v1","status":"Success"}`)
					return nil
				}
				if strings.Contains(args, "--ignore-not-found=true") {
					fmt.Fprint(cmd.Stdout, "owned")
					return nil
				}
				uid, marker := "owned", root.Marker
				if damage == "uid" {
					uid = "foreign"
				}
				if damage == "marker" {
					marker = "foreign"
				}
				timestamp := any(nil)
				if damage == "terminating" {
					timestamp = "2026-11-05T00:00:00Z"
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"kind": "Service", "apiVersion": "v1", "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "uid": uid, "resourceVersion": "9", "deletionTimestamp": timestamp, "annotations": map[string]any{modelOperationAnnotation: marker}}, "spec": map[string]any{}})
			}
			err := a.modelDeleteOwned(context.Background(), OrkaNamespace, root)
			if damage == "" || damage == "terminating" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe delete succeeded")
			}
			want := 1
			if damage == "uid" || damage == "marker" || damage == "terminating" {
				want = 0
			}
			if deletes != want {
				t.Fatalf("deletes %d want%d", deletes, want)
			}
			if err != nil && strings.Contains(err.Error(), "private-body") {
				t.Fatal("raw error surfaced")
			}
		})
	}
}

func TestOrkaModelChildren(t *testing.T) {
	root := modelOwnedResource{APIVersion: "apps/v1", Kind: "Deployment", Name: "gateway", UID: "gateway-uid", Spec: map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "gateway"}}, "template": map[string]any{"spec": map[string]any{"serviceAccountName": "default", "containers": []any{map[string]any{"name": "gateway", "image": "pinned"}}}}}}
	rs := map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": "gateway-rs", "namespace": OrkaNamespace, "uid": "rs-uid", "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "gateway", "uid": "gateway-uid", "controller": true}}, "labels": map[string]any{"app": "gateway"}}, "spec": map[string]any{"template": root.Spec["template"]}}
	for _, damage := range []string{"", "foreign-owner", "spec", "incomplete"} {
		t.Run(damage, func(t *testing.T) {
			raw, _ := json.Marshal(rs)
			var child map[string]any
			json.Unmarshal(raw, &child)
			switch damage {
			case "foreign-owner":
				modelMap(modelMap(child["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
			case "spec":
				child["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{"serviceAccountName": "other"}}}
			}
			a := New(&config.Config{})
			a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				items := []any{}
				kind, api := "PodList", "v1"
				if strings.Contains(args, "replicasets") {
					kind, api = "ReplicaSetList", "apps/v1"
					items = append(items, child)
				}
				meta := map[string]any{}
				if damage == "incomplete" {
					meta["continue"] = "next"
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": api, "kind": kind, "metadata": meta, "items": items})
			}
			children, err := a.modelChildren(context.Background(), OrkaNamespace, root)
			if damage == "" {
				if err != nil || len(children) != 1 {
					t.Fatalf("closure: %v %v", children, err)
				}
			} else if err == nil {
				t.Fatal("unsafe closure accepted")
			}
		})
	}
}
