package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func modelDisconnectOptions(id string) OrkaModelDisconnectOptions {
	return OrkaModelDisconnectOptions{OperationID: "33333333-3333-4333-8333-333333333333", Namespace: OrkaNamespace, Provider: "copilot", Connection: id}
}
func persistentModelCreate(cmd *exec.Cmd) bool {
	return slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server")
}

func TestOrkaModelPreflightFailures(t *testing.T) {
	for _, damage := range []string{"credential", "admission", "admitted-spec", "occupied"} {
		t.Run(damage, func(t *testing.T) {
			a, _, _ := modelConfigureFixture(t)
			capture := a.captureCommand
			creates := 0
			if damage == "credential" {
				bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
				if e := os.WriteFile(filepath.Join(bin, "stat"), []byte("#!/bin/sh\nexit 1\n"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if persistentModelCreate(cmd) {
					creates++
				}
				if damage == "occupied" && slices.Contains(cmd.Args, "--ignore-not-found=true") {
					fmt.Fprint(cmd.Stdout, "foreign")
					return nil
				}
				if slices.Contains(cmd.Args, "--dry-run=server") {
					if damage == "admission" {
						return fmt.Errorf("private-denial")
					}
					if damage == "admitted-spec" {
						var doc map[string]any
						json.NewDecoder(cmd.Stdin).Decode(&doc)
						modelMap(doc["spec"])["automountServiceAccountToken"] = true
						return json.NewEncoder(cmd.Stdout).Encode(doc)
					}
				}
				return capture(ctx, cmd)
			}
			result, err := a.ConnectOrkaModel(modelConnectOptions())
			if err == nil || result.Connection != nil || creates != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe preflight %s: %+v %v creates%d", damage, result, err, creates)
			}
		})
	}
}

func TestOrkaModelPodAdmissionLabels(t *testing.T) {
	a, _, _ := modelConfigureFixture(t)
	capture := a.captureCommand
	seen := false
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "--dry-run=server") {
			raw, _ := io.ReadAll(cmd.Stdin)
			var pod map[string]any
			json.Unmarshal(raw, &pod)
			seen = true
			if modelMap(modelMap(pod["metadata"])["labels"])[modelOperationLabel] != modelConnectOptions().OperationID {
				t.Error("admission did not evaluate actual operation labels")
			}
			cmd.Stdin = strings.NewReader(string(raw))
		}
		return capture(ctx, cmd)
	}
	if _, err := a.ConnectOrkaModel(modelConnectOptions()); err != nil || !seen {
		t.Fatalf("admission boundary not reached: %v", err)
	}
}

func TestOrkaModelChangedConnection(t *testing.T) {
	for _, damage := range []string{"generation", "uid", "image", "storage", "service-account"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, calls := modelConfigureFixture(t)
			opt := modelConnectOptions()
			first, err := a.ConnectOrkaModel(opt)
			if err != nil {
				t.Fatal(err)
			}
			dep := objects["/apis/apps/v1/namespaces/orka-system/deployments/copilot-vekil"]
			switch damage {
			case "generation":
				opt.CredentialGeneration = "44444444-4444-4444-8444-444444444444"
			case "uid":
				modelMap(dep["metadata"])["uid"] = "replacement"
			case "image":
				modelMap(modelMap(modelMap(dep["spec"])["template"])["spec"])["containers"].([]any)[0].(map[string]any)["image"] = "foreign"
			case "storage":
				modelMap(modelMap(modelMap(dep["spec"])["template"])["spec"])["volumes"] = []any{}
			case "service-account":
				modelMap(modelMap(modelMap(dep["spec"])["template"])["spec"])["serviceAccountName"] = "foreign"
			}
			before := len(*calls)
			r, err := a.ConnectOrkaModel(opt)
			if err == nil || r.Connection == nil || r.Connection.ID != first.Connection.ID || r.Connection.State != "unknown" {
				t.Fatalf("changed connection accepted %+v %v", r, err)
			}
			if damage != "generation" {
				if _, err = a.DisconnectOrkaModel(modelDisconnectOptions(first.Connection.ID)); err == nil {
					t.Fatal("changed ownership deleted")
				}
			}
			for _, call := range (*calls)[before:] {
				if strings.Contains(call, " create ") || strings.Contains(call, " delete ") {
					t.Fatal("changed connection mutated", call)
				}
			}
		})
	}
}

func TestOrkaModelAuthFailureRecovery(t *testing.T) {
	a, objects, _ := modelConfigureFixture(t)
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.HasSuffix(cmd.Args[len(cmd.Args)-1], "/readyz") {
			return fmt.Errorf("expired private-auth")
		}
		return capture(ctx, cmd)
	}
	failed, err := a.ConnectOrkaModel(modelConnectOptions())
	if err == nil || failed.Connection == nil || failed.Connection.State != "unknown" {
		t.Fatal("lost failed connection ID")
	}
	a.captureCommand = capture
	modelDeleteFixture(a, objects)
	if _, err = a.DisconnectOrkaModel(modelDisconnectOptions(failed.Connection.ID)); err != nil {
		t.Fatal(err)
	}
	opt := modelConnectOptions()
	opt.OperationID = "55555555-5555-4555-8555-555555555555"
	opt.CredentialGeneration = "44444444-4444-4444-8444-444444444444"
	fresh, err := a.ConnectOrkaModel(opt)
	if err != nil || fresh.Connection == nil || fresh.Connection.ID == failed.Connection.ID || fresh.Connection.CredentialGeneration != opt.CredentialGeneration {
		t.Fatalf("fresh connection failed: %+v %v", fresh, err)
	}
}

func TestOrkaModelAmbiguousDisconnect(t *testing.T) {
	for _, damage := range []string{"intact", "absent", "replaced", "terminating", "old-request-race"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, _ := modelConfigureFixture(t)
			a.waitTiming = &waitTiming{pollInterval: time.Millisecond, timeout: func(ctx context.Context, phase string, duration time.Duration) (context.Context, context.CancelFunc) {
				if phase == "orka-model-delete" {
					duration = 40 * time.Millisecond
				}
				return context.WithTimeout(ctx, duration)
			}}
			connected, err := a.ConnectOrkaModel(modelConnectOptions())
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			depPath := "/apis/apps/v1/namespaces/orka-system/deployments/copilot-vekil"
			deletes, depDeletes := 0, 0
			first := true
			var oldConditions map[string]any
			oldRejected := false
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "delete") {
					deletes++
					path := cmd.Args[slices.Index(cmd.Args, "--raw")+1]
					var body map[string]any
					json.NewDecoder(cmd.Stdin).Decode(&body)
					if path == depPath {
						depDeletes++
						if first {
							first = false
							oldConditions = modelMap(body["preconditions"])
							if modelMap(body["preconditions"])["resourceVersion"] != "3" {
								t.Fatal("initial RV changed")
							}
							if damage == "absent" {
								delete(objects, path)
							}
							return fmt.Errorf("lost private-ack")
						}
						if modelMap(body["preconditions"])["resourceVersion"] != "4" {
							t.Fatal("fresh invocation used stale RV")
						}
						if damage == "old-request-race" {
							// Deliver the old pending request after reinspection, before this send.
							// Model the API's UID/RV preconditions, not a second KMX invocation.
							if oldConditions["uid"] == modelMap(objects[path]["metadata"])["uid"] && oldConditions["resourceVersion"] == modelMap(objects[path]["metadata"])["resourceVersion"] {
								t.Fatal("stale pending request could delete reobserved root")
							}
							oldRejected = true
						}
					}
					if modelMap(body["preconditions"])["uid"] != modelMap(objects[path]["metadata"])["uid"] {
						t.Fatal("fresh invocation lost UID condition")
					}
					delete(objects, path)
					fmt.Fprint(cmd.Stdout, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
					return nil
				}
				return capture(ctx, cmd)
			}
			opt := modelDisconnectOptions(connected.Connection.ID)
			failed, err := a.DisconnectOrkaModel(opt)
			if err == nil || failed.Connection == nil || failed.RemoteState == "stopped" || deletes != 1 {
				t.Fatalf("lost delete misreported: %+v %v %d", failed, err, deletes)
			}
			if _, err = a.DisconnectOrkaModel(opt); err == nil || deletes != 1 {
				t.Fatal("same operation replayed DELETE")
			}
			if dep := objects[depPath]; dep != nil {
				modelMap(dep["metadata"])["resourceVersion"] = "4"
				if damage == "replaced" {
					modelMap(dep["metadata"])["uid"] = "foreign"
				}
				if damage == "terminating" {
					modelMap(dep["metadata"])["deletionTimestamp"] = "2026-11-05T00:00:00Z"
				}
			}
			opt.OperationID = "44444444-4444-4444-8444-444444444444"
			r, err := a.DisconnectOrkaModel(opt)
			if damage == "replaced" || damage == "terminating" {
				if err == nil || deletes != 1 || r.RemoteState == "stopped" {
					t.Fatalf("unsafe recovery: %+v %v sends%d", r, err, deletes)
				}
				return
			}
			if damage == "old-request-race" && !oldRejected {
				t.Fatal("race boundary not reached")
			}
			want := 2
			if damage == "absent" {
				want = 1
			}
			if err != nil || r.Connection == nil || r.Connection.State != "gateway_stopped" || depDeletes != want {
				t.Fatalf("recovery failed: %+v %v depDELETEs%d", r, err, depDeletes)
			}
		})
	}
}

func TestOrkaModelPreflightNeverAssumesStopped(t *testing.T) {
	for _, damage := range []string{"secret-owner", "read-denied"} {
		t.Run(damage, func(t *testing.T) {
			a, objects, _ := modelConfigureFixture(t)
			connected, err := a.ConnectOrkaModel(modelConnectOptions())
			if err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			deletes := 0
			secret := "/api/v1/namespaces/orka-system/secrets/copilot-model-key"
			for path, obj := range objects {
				if obj["kind"] == "Secret" {
					secret = path
					if damage == "secret-owner" {
						modelMap(obj["metadata"])["uid"] = "replacement"
					}
				}
			}
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "delete") {
					deletes++
				}
				if damage == "read-denied" && strings.HasSuffix(strings.Join(cmd.Args, " "), secret) {
					return fmt.Errorf("denied")
				}
				return capture(ctx, cmd)
			}
			r, err := a.DisconnectOrkaModel(modelDisconnectOptions(connected.Connection.ID))
			if err == nil || r.RemoteState == "stopped" || deletes != 0 {
				t.Fatalf("preflight failure falsely stopped live gateway: %+v %v sends%d", r, err, deletes)
			}
		})
	}
}

func TestOrkaModelCancellationBoundaries(t *testing.T) {
	for _, when := range []string{"before", "gateway", "after-create"} {
		t.Run(when, func(t *testing.T) {
			a, _, _ := modelConfigureFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.Run.Context = ctx
			capture := a.captureCommand
			creates := 0
			if when == "before" {
				cancel()
			}
			if when == "gateway" {
				a.operationProgress = func(label, status string, _ error) {
					if label == "Model gateway" && status == "active" {
						cancel()
					}
				}
			}
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				err := capture(ctx, cmd)
				if persistentModelCreate(cmd) {
					creates++
					if when == "after-create" {
						cancel()
					}
				}
				return err
			}
			r, err := a.ConnectOrkaModel(modelConnectOptions())
			want := 0
			if when == "after-create" {
				want = 1
			}
			if err == nil || creates != want || r.Connection != nil && r.Connection.State != "unknown" {
				t.Fatalf("cancellation sent later effect: %+v %v %d", r, err, creates)
			}
			if when != "before" && r.Connection == nil {
				t.Fatal("anchored ID lost on cancellation")
			}
		})
	}
}
