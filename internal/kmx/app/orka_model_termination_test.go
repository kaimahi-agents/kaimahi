package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// This models the registry's foreground-deletion metadata update, not an
// execution-spec change. Immediate removal hides this observation boundary.
func modelTerminationObservation(kind string) (modelOwnedResource, map[string]any) {
	version := "core.orka.ai/v1alpha1"
	if kind == "Deployment" {
		version = "apps/v1"
	}
	marker := "11111111-1111-4111-8111-111111111111"
	want := modelOwnedResource{APIVersion: version, Kind: kind, Name: "model", UID: "owned", ResourceVersion: "7", Marker: marker, Purpose: "connection", Generation: 1, Spec: map[string]any{}}
	obj := map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": "model", "namespace": OrkaNamespace, "uid": "owned", "resourceVersion": "8", "generation": float64(2), "annotations": map[string]any{modelOperationAnnotation: marker}, "deletionTimestamp": "2026-11-16T22:36:05Z", "deletionGracePeriodSeconds": float64(0), "finalizers": []any{"foregroundDeletion"}}, "spec": map[string]any{}}
	return want, obj
}

func TestOrkaModelObserveForegroundTermination(t *testing.T) {
	for _, kind := range []string{"Deployment", "Provider", "Agent", "Task"} {
		t.Run(kind, func(t *testing.T) {
			want, obj := modelTerminationObservation(kind)
			got, err := modelObserved(OrkaNamespace, want, obj)
			if err != nil || !got.Terminating || got.Generation != 2 || got.ResourceVersion != "8" || got.UID != "owned" {
				t.Fatalf("native terminating observation rejected: %+v %v", got, err)
			}
		})
	}
}

func TestOrkaModelForegroundTerminationRefusesDrift(t *testing.T) {
	for _, damage := range []string{"nondeleting", "null-timestamp", "invalid-timestamp", "missing-grace", "nonzero-grace", "negative-grace", "string-grace", "generation-jump", "invalid-generation", "fractional-generation", "uid", "marker", "spec", "already-terminating", "overflow"} {
		t.Run(damage, func(t *testing.T) {
			want, obj := modelTerminationObservation("Deployment")
			meta := modelMap(obj["metadata"])
			switch damage {
			case "nondeleting":
				delete(meta, "deletionTimestamp")
			case "null-timestamp":
				meta["deletionTimestamp"] = nil
			case "invalid-timestamp":
				meta["deletionTimestamp"] = "invalid"
			case "missing-grace":
				delete(meta, "deletionGracePeriodSeconds")
			case "nonzero-grace":
				meta["deletionGracePeriodSeconds"] = float64(30)
			case "negative-grace":
				meta["deletionGracePeriodSeconds"] = float64(-1)
			case "string-grace":
				meta["deletionGracePeriodSeconds"] = "0"
			case "generation-jump":
				meta["generation"] = float64(3)
			case "invalid-generation":
				meta["generation"] = float64(0)
			case "fractional-generation":
				meta["generation"] = 2.5
			case "uid":
				meta["uid"] = "foreign"
			case "marker":
				meta["annotations"] = map[string]any{modelOperationAnnotation: "foreign"}
			case "spec":
				obj["spec"] = map[string]any{"replicas": float64(2)}
			case "already-terminating":
				want.Terminating = true
			case "overflow":
				want.Generation = math.MaxInt64
				meta["generation"] = float64(math.MinInt64)
			}
			if _, err := modelObserved(OrkaNamespace, want, obj); err == nil {
				t.Fatal("accepted changed or non-native terminating object")
			}
		})
	}
}

func TestOrkaModelForegroundTerminationKeepsUnchangedGeneration(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		want, obj := modelTerminationObservation("Deployment")
		meta := modelMap(obj["metadata"])
		meta["generation"] = float64(1)
		if !deleting {
			delete(meta, "deletionTimestamp")
			delete(meta, "deletionGracePeriodSeconds")
		}
		got, err := modelObserved(OrkaNamespace, want, obj)
		if err != nil || got.Terminating != deleting || got.Generation != 1 {
			t.Fatalf("unchanged generation behavior changed: %+v %v", got, err)
		}
	}
}

func TestOrkaModelTerminatingGenerationDoesNotAuthorizeDelete(t *testing.T) {
	want, obj := modelTerminationObservation("Deployment")
	a := New(&config.Config{})
	sends := 0
	a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "delete") {
			sends++
			return fmt.Errorf("terminating observation authorized another DELETE")
		}
		if slices.Contains(cmd.Args, "--ignore-not-found=true") {
			fmt.Fprint(cmd.Stdout, "owned")
			return nil
		}
		return json.NewEncoder(cmd.Stdout).Encode(obj)
	}
	err := a.modelDeleteOwned(context.Background(), OrkaNamespace, want)
	if err != nil || sends != 0 {
		t.Fatalf("terminating resource was not observation-only: sends%d err%v", sends, err)
	}
}

func TestOrkaModelPendingForegroundTerminationDoesNotResend(t *testing.T) {
	a, objects, _ := modelLifecycleFixture(t)
	connected, err := a.ConnectOrkaModel(modelConnectOptions())
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	sends, waits := 0, 0
	a.waitTiming = &waitTiming{timeout: func(ctx context.Context, stage string, d time.Duration) (context.Context, context.CancelFunc) {
		if stage == "orka-model-delete" {
			waits++
			d = 10 * time.Millisecond
		}
		return context.WithTimeout(ctx, d)
	}}
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if !slices.Contains(cmd.Args, "delete") {
			return capture(ctx, cmd)
		}
		sends++
		if sends != 1 {
			return fmt.Errorf("replayed ambiguous foreground DELETE")
		}
		path := cmd.Args[slices.Index(cmd.Args, "--raw")+1]
		meta := modelMap(objects[path]["metadata"])
		meta["generation"] = float64(2)
		meta["resourceVersion"] = "4"
		meta["deletionTimestamp"] = "2026-11-16T22:36:05Z"
		meta["deletionGracePeriodSeconds"] = float64(0)
		meta["finalizers"] = []any{"foregroundDeletion"}
		return fmt.Errorf("lost acknowledgment")
	}
	opt := modelDisconnectOptions(connected.Connection.ID)
	if _, err = a.DisconnectOrkaModel(opt); err == nil {
		t.Fatal("lost acknowledgment reported complete")
	}
	opt.OperationID = "44444444-4444-4444-8444-444444444444"
	result, err := a.DisconnectOrkaModel(opt)
	if err == nil || sends != 1 || waits != 1 || result.RemoteState == "stopped" {
		t.Fatalf("pending termination was not observed fail-closed without replay: sends%d waits%d result%+v err%v", sends, waits, result, err)
	}
}

func TestOrkaModelDisconnectForegroundTermination(t *testing.T) {
	a, objects, _ := modelLifecycleFixture(t)
	connected, err := a.ConnectOrkaModel(modelConnectOptions())
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	deleting := map[string]int{}
	sends := map[string]int{}
	terminatingReads := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "delete") {
			i := slices.Index(cmd.Args, "--raw")
			if i < 0 {
				t.Fatal("DELETE lacked raw resource path")
			}
			path := cmd.Args[i+1]
			sends[path]++
			if sends[path] != 1 {
				return fmt.Errorf("repeated conditional DELETE")
			}
			obj := objects[path]
			if obj == nil {
				return fmt.Errorf("DELETE without owned object")
			}
			meta := modelMap(obj["metadata"])
			var body map[string]any
			if err := json.NewDecoder(cmd.Stdin).Decode(&body); err != nil {
				return err
			}
			pre := modelMap(body["preconditions"])
			if body["propagationPolicy"] != "Foreground" || pre["uid"] != meta["uid"] || pre["resourceVersion"] != meta["resourceVersion"] {
				return fmt.Errorf("lost foreground UID/RV preconditions")
			}
			// Match the native registry transition; keep it visible for one wait read.
			if generation, ok := meta["generation"].(float64); ok && generation > 0 {
				meta["generation"] = generation + 1
			}
			meta["resourceVersion"] = "4"
			meta["deletionTimestamp"] = "2026-11-16T22:36:05Z"
			meta["deletionGracePeriodSeconds"] = float64(0)
			meta["finalizers"] = []any{"foregroundDeletion"}
			deleting[path] = 0
			return json.NewEncoder(cmd.Stdout).Encode(obj)
		}
		if slices.Contains(cmd.Args, "--ignore-not-found=true") {
			get := slices.Index(cmd.Args, "get")
			resource, name := cmd.Args[get+1], cmd.Args[get+2]
			for path, reads := range deleting {
				obj := objects[path]
				if obj == nil {
					continue
				}
				meta := modelMap(obj["metadata"])
				qualified := map[string]string{"Deployment": "deployments.apps", "Provider": "providers.core.orka.ai", "Service": "services", "Secret": "secrets"}[modelText(obj["kind"])]
				if resource != qualified || meta["name"] != name {
					continue
				}
				deleting[path] = reads + 1
				if reads >= 1 {
					delete(objects, path)
					return nil
				}
				terminatingReads++
			}
		}
		return capture(ctx, cmd)
	}
	result, err := a.DisconnectOrkaModel(modelDisconnectOptions(connected.Connection.ID))
	if err != nil || result.Connection == nil || result.Connection.State != "gateway_stopped" || result.RemoteState != "stopped" {
		t.Fatalf("foreground cleanup did not complete: %+v %v", result, err)
	}
	if len(objects) != 0 || len(sends) != 4 || terminatingReads != 4 {
		t.Fatalf("cleanup omitted native observations/resources: remaining%d sends%v reads%d", len(objects), sends, terminatingReads)
	}
}
