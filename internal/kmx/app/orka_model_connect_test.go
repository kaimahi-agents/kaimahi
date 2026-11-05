package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestOrkaModelDisconnect(t *testing.T) {
	a, objects, calls := modelConfigureFixture(t)
	connected, err := a.ConnectOrkaModel(OrkaModelConnectOptions{OperationID: "11111111-1111-4111-8111-111111111111", Namespace: OrkaNamespace, Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	capture := a.captureCommand
	deleted := []string{}
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
			if modelMap(body["preconditions"])["uid"] != modelMap(objects[path]["metadata"])["uid"] || modelMap(body["preconditions"])["resourceVersion"] != "3" {
				t.Fatal("delete not UID/RV bound")
			}
			delete(objects, path)
			fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
			return nil
		}
		return capture(ctx, cmd)
	}
	result, err := a.DisconnectOrkaModel(OrkaModelDisconnectOptions{OperationID: "33333333-3333-4333-8333-333333333333", Namespace: OrkaNamespace, Provider: "copilot", Connection: connected.Connection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Connection == nil || result.Connection.State != "gateway_stopped" || len(objects) != 0 || len(deleted) != 4 || !strings.Contains(deleted[0], "/deployments/") {
		t.Fatalf("cleanup incomplete: %+v %+v", result, deleted)
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
			a, _, calls := modelConfigureFixture(t)
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
		a, objects, calls := modelConfigureFixture(t)
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
		a, _, calls := modelConfigureFixture(t)
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
