package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestOrkaModelCaseSensitiveChildMetadata(t *testing.T) {
	for _, operation := range []string{"connect", "disconnect"} {
		for _, kind := range []string{"ReplicaSet", "Pod"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				a, objects, _ := modelLifecycleFixture(t)
				modelDeleteFixture(a, objects)
				var connected OrkaModelResult
				var err error
				if operation == "disconnect" {
					connected, err = a.ConnectOrkaModel(modelConnectOptions())
					if err != nil {
						t.Fatal(err)
					}
				}
				capture := a.captureCommand
				a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					path := "/replicasets?limit=64"
					if kind == "Pod" {
						path = "/pods?limit=64"
					}
					if !strings.Contains(strings.Join(cmd.Args, " "), path) {
						return capture(ctx, cmd)
					}
					stdout := cmd.Stdout
					var raw bytes.Buffer
					cmd.Stdout = &raw
					err := capture(ctx, cmd)
					cmd.Stdout = stdout
					if err != nil {
						return err
					}
					var list map[string]any
					if err = json.Unmarshal(raw.Bytes(), &list); err != nil {
						return err
					}
					// Server-side apply records uppercase and lowercase proxy variables as
					// distinct map keys on an unrelated workload in the same namespace.
					unrelated := map[string]any{
						"apiVersion": "v1", "kind": kind,
						"metadata": map[string]any{
							"name": "workspace-publisher", "namespace": OrkaNamespace, "uid": "publisher-uid",
							"managedFields": []any{map[string]any{"fieldsType": "FieldsV1", "fieldsV1": map[string]any{
								"f:spec": map[string]any{"f:containers": map[string]any{"k:{\"name\":\"publisher\"}": map[string]any{
									"f:env": map[string]any{"k:{\"name\":\"HTTPS_PROXY\"}": map[string]any{}, "k:{\"name\":\"https_proxy\"}": map[string]any{}},
								}}},
							}}},
						},
					}
					if kind == "ReplicaSet" {
						unrelated["apiVersion"] = "apps/v1"
					}
					list["items"] = append(list["items"].([]any), unrelated)
					return json.NewEncoder(stdout).Encode(list)
				}
				if operation == "connect" {
					connected, err = a.ConnectOrkaModel(modelConnectOptions())
					if err != nil || connected.Connection == nil || connected.Connection.State != "model_ready" {
						t.Fatalf("valid child metadata blocked Connect: %+v %v", connected, err)
					}
				} else {
					result, err := a.DisconnectOrkaModel(modelDisconnectOptions(connected.Connection.ID))
					if err != nil || result.Connection == nil || result.Connection.State != "gateway_stopped" {
						t.Fatalf("valid child metadata blocked Disconnect: %+v %v", result, err)
					}
				}
			})
		}
	}
}
