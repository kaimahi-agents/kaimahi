package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestOrkaModelSecondObservationAbsence(t *testing.T) {
	for _, kind := range []string{"ReplicaSet", "Pod", "Service", "Secret", "Deployment", "Provider", "Agent", "Task"} {
		for _, damage := range []string{"present", "absent-first", "absent-second", "wrong-first-uid", "error-first", "cancel-first", "error-second", "cancel-second", "whitespace", "malformed", "null", "list", "kind", "version", "name", "namespace", "uid", "owner"} {
			t.Run(kind+"/"+damage, func(t *testing.T) {
				child := kind == "ReplicaSet" || kind == "Pod"
				version, plural := "v1", "pods"
				if kind == "ReplicaSet" {
					version, plural = "apps/v1", "replicasets"
				}
				if !child {
					api, _ := modelAPIFor(kind)
					version, plural = api.version, api.plural
				}
				presenceResource, snapshotResource := plural, plural
				if group, v, grouped := strings.Cut(version, "/"); grouped {
					presenceResource += "." + group
					snapshotResource += "." + v + "." + group
				}
				marker := "11111111-1111-4111-8111-111111111111"
				meta := map[string]any{"name": "gateway", "namespace": OrkaNamespace, "uid": "retained-uid", "resourceVersion": "8", "generation": float64(1), "annotations": map[string]any{modelOperationAnnotation: marker}, "ownerReferences": []any{map[string]any{"uid": "retained-owner"}}}
				spec := map[string]any{}
				obj := map[string]any{"apiVersion": version, "kind": kind, "metadata": meta, "spec": spec}
				if kind == "Secret" {
					spec = map[string]any{"type": "Opaque", "data": map[string]any{}}
					delete(obj, "spec")
					obj["type"], obj["data"] = spec["type"], spec["data"]
				}
				want := modelOwnedResource{APIVersion: version, Kind: kind, Name: "gateway", UID: "retained-uid", ResourceVersion: "7", Marker: marker, Purpose: "connection", Generation: 1, Spec: spec}
				a := New(&config.Config{})
				calls, snapshots := 0, 0
				a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
					calls++
					presence := slices.Contains(cmd.Args, "jsonpath={.metadata.uid}")
					if presence {
						get := slices.Index(cmd.Args, "get")
						if cmd.Args[get+1] != presenceResource {
							t.Fatalf("presence resource=%s want%s", cmd.Args[get+1], presenceResource)
						}
						switch damage {
						case "absent-first":
							return nil
						case "error-first":
							return fmt.Errorf("private read denial")
						case "cancel-first":
							return context.Canceled
						case "wrong-first-uid":
							fmt.Fprint(cmd.Stdout, "reused-uid")
							return nil
						}
						fmt.Fprint(cmd.Stdout, "retained-uid")
						return nil
					}
					named := slices.Contains(cmd.Args, "--ignore-not-found=true") && slices.Contains(cmd.Args, "json")
					if named {
						snapshots++
						get := slices.Index(cmd.Args, "get")
						if cmd.Args[get+1] != snapshotResource {
							t.Fatalf("snapshot resource=%s want%s", cmd.Args[get+1], snapshotResource)
						}
					}
					switch damage {
					case "absent-second":
						if named {
							return nil
						}
						fmt.Fprint(cmd.Stderr, "Error from server (NotFound): object disappeared after presence")
						return fmt.Errorf("missing between reads")
					case "error-second":
						return fmt.Errorf("private read denial")
					case "cancel-second":
						return context.Canceled
					case "whitespace":
						fmt.Fprint(cmd.Stdout, " \n\t")
						return nil
					case "malformed":
						fmt.Fprint(cmd.Stdout, "{")
						return nil
					case "null":
						fmt.Fprint(cmd.Stdout, "null")
						return nil
					case "list":
						fmt.Fprint(cmd.Stdout, "[]")
						return nil
					case "kind":
						obj["kind"] = "Other"
					case "version":
						obj["apiVersion"] = "other/v1"
					case "name":
						meta["name"] = "other"
					case "namespace":
						meta["namespace"] = "other-system"
					case "uid":
						meta["uid"] = "reused-uid"
					case "owner":
						meta["ownerReferences"] = []any{map[string]any{"uid": "foreign-owner"}}
						meta["annotations"] = map[string]any{modelOperationAnnotation: "foreign"}
					}
					return json.NewEncoder(cmd.Stdout).Encode(obj)
				}
				var gone bool
				var err error
				if child {
					gone, err = a.modelChildrenGone(context.Background(), OrkaNamespace, []modelChild{{APIVersion: version, Kind: kind, Name: "gateway", UID: "retained-uid", OwnerUID: "retained-owner"}})
				} else {
					var got modelOwnedResource
					var present bool
					got, present, err = a.readModelObject(context.Background(), OrkaNamespace, want)
					gone = !present
					if damage == "present" && err == nil && got.ResourceVersion != "8" {
						t.Fatal("fresh resourceVersion lost")
					}
				}
				expectError := damage != "present" && damage != "absent-first" && damage != "absent-second"
				if (err != nil) != expectError || !expectError && gone != (damage != "present") {
					t.Fatalf("observation damage%s gone%v err%v", damage, gone, err)
				}
				wantCalls := 2
				if damage == "absent-first" || damage == "wrong-first-uid" || damage == "error-first" || damage == "cancel-first" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("reads%d want%d", calls, wantCalls)
				}
				if wantCalls == 1 && snapshots != 0 || damage == "present" && snapshots != 1 {
					t.Fatalf("wrong full-observation boundary: snapshots%d", snapshots)
				}
			})
		}
	}
}

func TestOrkaModelDisconnectChildDisappearsBetweenReads(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprint(running), func(t *testing.T) {
			a, objects, _ := modelLifecycleFixture(t)
			connected, err := a.ConnectOrkaModel(modelConnectOptions())
			if err != nil {
				t.Fatal(err)
			}
			var retained map[string]any
			for _, child := range modelFixtureChildren(objects) {
				if child["kind"] == "ReplicaSet" {
					retained = child
				}
			}
			if retained == nil {
				t.Fatal("missing retained ReplicaSet")
			}
			childMeta := modelMap(retained["metadata"])
			if running {
				t.Setenv("MODEL_CRI_RUNNING", "1")
			}
			modelDeleteFixture(a, objects)
			capture := a.captureCommand
			deleting, deletes, probes, snapshots := false, 0, 0, 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "delete") {
					deletes++
					path := cmd.Args[slices.Index(cmd.Args, "--raw")+1]
					var request map[string]any
					if err := json.NewDecoder(cmd.Stdin).Decode(&request); err != nil {
						t.Fatal(err)
					}
					meta := modelMap(objects[path]["metadata"])
					pre := modelMap(request["preconditions"])
					if request["propagationPolicy"] != "Foreground" || pre["uid"] != meta["uid"] || pre["resourceVersion"] != meta["resourceVersion"] {
						t.Fatal("lost conditional foreground delete authority")
					}
					raw, _ := json.Marshal(request)
					cmd.Stdin = bytes.NewReader(raw)
					if strings.Contains(path, "/deployments/") {
						deleting = true
					}
					return capture(ctx, cmd)
				}
				args := strings.Join(cmd.Args, " ")
				if deleting && strings.Contains(args, modelText(childMeta["name"])) {
					if slices.Contains(cmd.Args, "jsonpath={.metadata.uid}") {
						probes++
						fmt.Fprint(cmd.Stdout, childMeta["uid"])
						return nil
					}
					snapshots++
					if slices.Contains(cmd.Args, "--ignore-not-found=true") && slices.Contains(cmd.Args, "json") {
						return nil
					}
					fmt.Fprint(cmd.Stderr, "Error from server (NotFound): ReplicaSet disappeared after identity probe")
					return fmt.Errorf("native garbage collection completed")
				}
				return capture(ctx, cmd)
			}
			result, err := a.DisconnectOrkaModel(modelDisconnectOptions(connected.Connection.ID))
			if probes != 1 || snapshots != 1 {
				t.Fatalf("did not hit actual disappearance window: probes%d snapshots%d", probes, snapshots)
			}
			if running {
				stopRefused := false
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					stopRefused = stopRefused || cause.Error() == "model credential process stop is unknown"
				}
				if !stopRefused {
					t.Fatalf("did not reach the unchanged CRI stop guard: %v", err)
				}
				if err == nil || result.RemoteState == "stopped" || deletes != 1 || len(objects) != 3 {
					t.Fatalf("API absence bypassed live process: %+v %v deletes%d remaining%d", result, err, deletes, len(objects))
				}
			} else if err != nil || result.Connection == nil || result.Connection.State != "gateway_stopped" || result.RemoteState != "stopped" || deletes != 4 || len(objects) != 0 {
				t.Fatalf("native disappearance blocked full Disconnect: %+v %v deletes%d remaining%d", result, err, deletes, len(objects))
			}
		})
	}
}
