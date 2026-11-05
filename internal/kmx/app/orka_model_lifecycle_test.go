package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func modelConnectOptions() OrkaModelConnectOptions {
	return OrkaModelConnectOptions{OperationID: "11111111-1111-4111-8111-111111111111", Namespace: OrkaNamespace, Route: "copilot", Provider: "copilot", Model: "gpt-4.1", CredentialGeneration: "22222222-2222-4222-8222-222222222222"}
}
func modelDeleteFixture(a *App, objects map[string]map[string]any) {
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "delete") {
			i := slices.Index(cmd.Args, "--raw")
			delete(objects, cmd.Args[i+1])
			fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
			return nil
		}
		return capture(ctx, cmd)
	}
}
func TestOrkaModelServingOwnership(t *testing.T) {
	for _, damage := range []string{"no-pod", "foreign-pod", "not-ready"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, _ := modelLifecycleFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			a.Run.Context = ctx
			capture := a.captureCommand
			health := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if strings.Contains(args, "/proxy/") {
					health++
				}
				if damage == "not-ready" && strings.HasSuffix(args, "/pods/copilot-vekil-pod") {
					for _, p := range modelFixtureChildren(objects) {
						if p["kind"] == "Pod" {
							p["status"] = map[string]any{"phase": "Pending"}
							return json.NewEncoder(cmd.Stdout).Encode(p)
						}
					}
				}
				if strings.Contains(args, "/pods?limit=64") {
					items := []any{}
					for _, p := range modelFixtureChildren(objects) {
						if p["kind"] != "Pod" {
							continue
						}
						switch damage {
						case "no-pod":
							continue
						case "foreign-pod":
							modelMap(modelMap(p["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
						case "not-ready":
							p["status"] = map[string]any{"phase": "Pending"}
						}
						items = append(items, p)
					}
					return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{}, "items": items})
				}
				return capture(ctx, cmd)
			}
			result, err := a.ConnectOrkaModel(modelConnectOptions())
			if err == nil || result.Connection == nil || result.Connection.State != "unknown" || health != 0 {
				t.Fatalf("unsafe ready: %+v err%v probes%d", result, err, health)
			}
		})
	}
}
func TestOrkaModelAbsentGatewayIsNotStop(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			a, objects, _ := modelLifecycleFixture(t)
			capture := a.captureCommand
			if !retained {
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if strings.HasSuffix(cmd.Args[len(cmd.Args)-1], "/readyz") {
						return fmt.Errorf("auth unavailable")
					}
					return capture(ctx, cmd)
				}
			}
			connected, err := a.ConnectOrkaModel(modelConnectOptions())
			if retained && err != nil {
				t.Fatal(err)
			}
			if connected.Connection == nil {
				t.Fatal("missing anchored ID")
			}
			a.captureCommand = capture
			delete(objects, "/apis/apps/v1/namespaces/orka-system/deployments/copilot-vekil")
			if retained {
				t.Setenv("MODEL_CRI_RUNNING", "1")
			}
			modelDeleteFixture(a, objects)
			result, err := a.DisconnectOrkaModel(OrkaModelDisconnectOptions{OperationID: "33333333-3333-4333-8333-333333333333", Namespace: OrkaNamespace, Provider: "copilot", Connection: connected.Connection.ID})
			if err == nil || result.Connection == nil || result.Connection.State == "gateway_stopped" {
				t.Fatalf("false stop: %+v %v", result, err)
			}
		})
	}
}
func TestOrkaModelAuthAndCatalog(t *testing.T) {
	for _, damage := range []string{"auth", "catalog"} {
		t.Run(damage, func(t *testing.T) {
			a, _, _ := modelLifecycleFixture(t)
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				args := strings.Join(cmd.Args, " ")
				if damage == "auth" && strings.HasSuffix(args, "/readyz") {
					return fmt.Errorf("private-auth-body")
				}
				if damage == "catalog" && strings.HasSuffix(args, "/v1/models") {
					fmt.Fprint(cmd.Stdout, `{"data":[{"id":"other"}]}`)
					return nil
				}
				return capture(ctx, cmd)
			}
			r, err := a.ConnectOrkaModel(modelConnectOptions())
			if err == nil || r.Connection == nil || r.Connection.State != "unknown" || strings.Contains(err.Error(), "private") {
				t.Fatalf("false auth/catalog %+v %v", r, err)
			}
		})
	}
}
