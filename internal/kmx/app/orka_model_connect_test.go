package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestOrkaModelDisconnect(t *testing.T) {
	for _, mode := range []string{"foreground-static-pod", "child-race-stopped", "child-race-running"} {
		t.Run(mode, func(t *testing.T) { modelDisconnectCase(t, mode) })
	}
}

func modelDisconnectCase(t *testing.T, mode string) {
	t.Helper()
	a, objects, calls := modelLifecycleFixture(t, fmt.Sprintf(`{"containers":[{"state":"CONTAINER_RUNNING","labels":{"io.kubernetes.pod.uid":%q,"io.kubernetes.pod.namespace":"kube-system","io.kubernetes.pod.name":"kube-scheduler-test-control-plane"}}]}`, strings.Repeat("b2", 16)))
	connected, err := a.ConnectOrkaModel(OrkaModelConnectOptions{OperationID: "11111111-1111-4111-8111-111111111111", Namespace: OrkaNamespace, Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "child-race-running" {
		t.Setenv("MODEL_CRI_RUNNING", "1")
	}
	capture := a.captureCommand
	deleted := []string{}
	sends, fullReads := map[string]int{}, map[string]int{}
	terminating := map[string]bool{}
	probes, snapshots := 0, 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "delete") {
			callsValue := strings.Join(cmd.Args, " ")
			*calls = append(*calls, callsValue)
			i := slices.Index(cmd.Args, "--raw")
			path := cmd.Args[i+1]
			deleted = append(deleted, path)
			raw, _ := io.ReadAll(cmd.Stdin)
			var body map[string]any
			json.Unmarshal(raw, &body)
			obj := objects[path]
			meta := modelMap(obj["metadata"])
			pre := modelMap(body["preconditions"])
			if body["propagationPolicy"] != "Foreground" || pre["uid"] != meta["uid"] || pre["resourceVersion"] != meta["resourceVersion"] {
				t.Fatal("delete not foreground/current UID/RV bound")
			}
			kind := modelText(obj["kind"])
			sends[kind]++
			if sends[kind] != 1 {
				t.Fatal("repeated DELETE", kind)
			}
			if mode == "foreground-static-pod" {
				if generation, ok := meta["generation"].(float64); ok {
					meta["generation"] = generation + 1
				}
				meta["resourceVersion"], meta["deletionTimestamp"], meta["deletionGracePeriodSeconds"] = "4", "2026-11-16T22:36:05Z", float64(0)
				meta["finalizers"], terminating[path] = []any{"foregroundDeletion"}, true
				return json.NewEncoder(cmd.Stdout).Encode(obj)
			}
			delete(objects, path)
			fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
			return nil
		}
		args := strings.Join(cmd.Args, " ")
		if strings.HasPrefix(mode, "child-race-") && sends["Deployment"] == 1 && strings.Contains(args, "copilot-vekil-rs") {
			if slices.Contains(cmd.Args, "jsonpath={.metadata.uid}") {
				probes++
				fmt.Fprint(cmd.Stdout, "owned-rs")
				return nil
			}
			snapshots++
			if slices.Contains(cmd.Args, "--ignore-not-found=true") && slices.Contains(cmd.Args, "json") {
				return nil
			}
			return fmt.Errorf("ReplicaSet disappeared after identity probe")
		}
		get := slices.Index(cmd.Args, "get")
		for path := range terminating {
			parts := strings.Split(path, "/")
			if get < 0 || !slices.Contains(cmd.Args, "json") || !strings.HasPrefix(cmd.Args[get+1]+".", parts[len(parts)-2]+".") || cmd.Args[get+2] != parts[len(parts)-1] {
				continue
			}
			// Deliver a real terminating snapshot before native GC removes the root.
			err := capture(ctx, cmd)
			fullReads[modelText(objects[path]["kind"])]++
			delete(objects, path)
			delete(terminating, path)
			return err
		}
		return capture(ctx, cmd)
	}
	result, err := a.DisconnectOrkaModel(OrkaModelDisconnectOptions{OperationID: "33333333-3333-4333-8333-333333333333", Namespace: OrkaNamespace, Provider: "copilot", Connection: connected.Connection.ID})
	if strings.HasPrefix(mode, "child-race-") && (probes != 1 || snapshots != 1) {
		t.Fatalf("missed disappearance window: probes%d snapshots%d", probes, snapshots)
	}
	if mode == "child-race-running" {
		stopRefused := false
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			stopRefused = stopRefused || cause.Error() == "model credential process stop is unknown"
		}
		if !stopRefused || result.RemoteState == "stopped" || len(objects) != 3 || len(deleted) != 1 || sends["Deployment"] != 1 {
			t.Fatalf("CRI guard not reached/intact: %+v %v sends%v roots%d", result, err, sends, len(objects))
		}
		return
	}
	if err != nil || result.Connection == nil || result.Connection.State != "gateway_stopped" || result.RemoteState != "stopped" || len(objects) != 0 || len(deleted) != 4 || !strings.Contains(deleted[0], "/deployments/") {
		t.Fatalf("cleanup incomplete: %+v %v %+v", result, err, deleted)
	}
	for _, kind := range []string{"Deployment", "Provider", "Service", "Secret"} {
		if sends[kind] != 1 || mode == "foreground-static-pod" && fullReads[kind] != 1 {
			t.Fatalf("missing DELETE/native full read: sends%v reads%v", sends, fullReads)
		}
	}
	for _, call := range *calls {
		if strings.Contains(call, " delete ") && !strings.Contains(call, "--raw") {
			t.Fatal("unconditioned delete")
		}
	}
}

func TestOrkaModelConnect(t *testing.T) {
	opt := OrkaModelConnectOptions{OperationID: "11111111-1111-4111-8111-111111111111", Namespace: OrkaNamespace, Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222"}
	t.Run("explicit selectors zero effects", func(t *testing.T) {
		for _, field := range []string{"operation", "namespace", "route", "provider", "model", "automatic model", "generation"} {
			a, _, calls := modelLifecycleFixture(t)
			bad := opt
			switch field {
			case "operation":
				bad.OperationID = "not-a-uuid"
			case "namespace":
				bad.Namespace = "other"
			case "route":
				bad.Route = "auto"
			case "provider":
				bad.Provider = ""
			case "model":
				bad.Model = ""
			case "automatic model":
				bad.Model = "auto"
			case "generation":
				bad.CredentialGeneration = ""
			}
			if _, err := a.ConnectOrkaModel(bad); err == nil {
				t.Errorf("accepted %s", field)
			}
			if len(*calls) != 0 {
				t.Fatal("invalid arguments sent API requests")
			}
		}
	})
	t.Run("configure then identical reconnect no mutations", func(t *testing.T) {
		a, objects, calls := modelLifecycleFixture(t)
		result, err := a.ConnectOrkaModel(opt)
		if err != nil {
			t.Fatal(err)
		}
		if result.Connection == nil || result.Connection.State != "model_ready" || !modelUUID(result.Connection.ID) || result.Proof != nil || len(objects) != 4 {
			t.Fatalf("invalid result %+v roots%d", result, len(objects))
		}
		before := len(*calls)
		again, err := a.ConnectOrkaModel(opt)
		if err != nil || again.Connection.ID != result.Connection.ID {
			t.Fatalf("reconnect %+v %v", again, err)
		}
		for _, call := range (*calls)[before:] {
			if strings.Contains(call, " create ") || strings.Contains(call, " delete ") {
				t.Fatal("reconnect mutated")
			}
		}
		for _, call := range *calls {
			for _, bad := range []string{" tasks ", " agents ", "apply", "helm", "ollama", "chat/completions", "responses"} {
				if strings.Contains(call, bad) {
					t.Fatal("forbidden connect effect " + call)
				}
			}
		}
	})
	t.Run("lost create retains ID and no replay", func(t *testing.T) {
		a, _, calls := modelLifecycleFixture(t)
		capture := a.captureCommand
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
				if err := capture(ctx, cmd); err != nil {
					return err
				}
				return fmt.Errorf("lost")
			}
			return capture(ctx, cmd)
		}
		result, err := a.ConnectOrkaModel(opt)
		if err == nil || result.Connection == nil || result.Connection.State != "unknown" {
			t.Fatalf("lost result %+v %v", result, err)
		}
		before := len(*calls)
		_, err = a.ConnectOrkaModel(opt)
		if err == nil {
			t.Fatal("replayed ambiguous create")
		}
		if _, err = a.DisconnectOrkaModel(modelDisconnectOptions(result.Connection.ID)); err == nil {
			t.Fatal("ambiguous CREATE adopted for cleanup")
		}
		for _, call := range (*calls)[before:] {
			if strings.Contains(call, " create ") || strings.Contains(call, " delete ") || strings.Contains(call, " get secrets ") {
				t.Fatal("ambiguous CREATE replayed or adopted", call)
			}
		}
	})
}
