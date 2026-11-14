package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

func TestOrkaModelPodAdmissionBuildIdentity(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, annotated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/annotations=%t", arch, annotated), func(t *testing.T) {
				a, _, _ := modelLifecycleFixture(t)
				a.buildIdentity = version.Build{Version: "v0.9.0-test", Release: true}
				docs, err := modelDocuments("copilot", "gpt-4.1", modelConnectOptions().OperationID, arch, "")
				if err != nil {
					t.Fatal(err)
				}
				templateMeta := modelMap(modelMap(modelMap(docs[1]["spec"])["template"])["metadata"])
				if annotated {
					templateMeta["annotations"] = map[string]any{"example.com/note": "preserved"}
				}
				before, err := json.Marshal(docs[1])
				if err != nil {
					t.Fatal(err)
				}
				admissions := 0
				a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
					if !slices.Contains(cmd.Args, "create") || !slices.Contains(cmd.Args, "--dry-run=server") {
						t.Fatalf("not server admission: %v", cmd.Args)
					}
					raw, err := io.ReadAll(cmd.Stdin)
					if err != nil {
						return err
					}
					var pod map[string]any
					if err := json.Unmarshal(raw, &pod); err != nil {
						return err
					}
					meta := modelMap(pod["metadata"])
					annotations := modelMap(meta["annotations"])
					if pod["kind"] != "Pod" || meta["name"] != "copilot-vekil-preflight" || meta["namespace"] != OrkaNamespace || annotations[createdByVersionAnnotation] != "v0.9.0-test" {
						t.Fatalf("invalid build-annotated Pod: %s", raw)
					}
					if annotated && annotations["example.com/note"] != "preserved" {
						t.Fatal("template annotation lost")
					}
					labels := modelMap(meta["labels"])
					if labels["kaimahi.dev/model-operation"] != modelConnectOptions().OperationID {
						t.Fatalf("operation label lost: %v", labels)
					}
					admissions++
					_, err = cmd.Stdout.Write(raw)
					return err
				}
				if err := a.modelPodAdmission(context.Background(), docs[1]); err != nil {
					t.Fatal(err)
				}
				if admissions != 1 {
					t.Fatalf("admissions = %d, want 1", admissions)
				}
				after, _ := json.Marshal(docs[1])
				if !bytes.Equal(before, after) {
					t.Fatal("Pod admission changed the Deployment template")
				}
			})
		}
	}
}

func TestOrkaModelConnectBuildIdentityPreflight(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject=%t", reject), func(t *testing.T) {
			a, objects, _ := modelLifecycleFixture(t)
			a.buildIdentity = version.Build{Version: "v0.9.0-test", Release: true}
			capture := a.captureCommand
			admitted := false
			preflights := 0
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "create") {
					if slices.Contains(cmd.Args, "--dry-run=server") {
						preflights++
						if reject {
							return fmt.Errorf("admission refused")
						}
						admitted = true
					} else if !admitted {
						t.Fatal("persistent CREATE before Pod admission")
					}
				}
				return capture(ctx, cmd)
			}
			result, err := a.ConnectOrkaModel(modelConnectOptions())
			if reject {
				if err == nil || preflights != 1 || result.Connection != nil || len(objects) != 0 {
					t.Fatalf("rejected admission had effects: %+v err=%v objects=%d", result, err, len(objects))
				}
				return
			}
			if err != nil || result.Connection == nil || result.Connection.State != "model_ready" || len(objects) != 4 {
				t.Fatalf("release-build Connect failed: %+v err=%v objects=%d", result, err, len(objects))
			}
		})
	}
}
