package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func modelSetCRI(t *testing.T, containers ...map[string]any) {
	t.Helper()
	tool := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "docker")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"containers": containers})
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), `printf '{"containers":[]}'`, "printf '%s' '"+string(body)+"'", 1)
	if script == string(raw) {
		t.Fatal("CRI fixture was not replaced")
	}
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func modelCRIContainer(uid, namespace, name, state string) map[string]any {
	return map[string]any{"state": state, "labels": map[string]any{"io.kubernetes.pod.uid": uid, "io.kubernetes.pod.namespace": namespace, "io.kubernetes.pod.name": name}}
}

func TestOrkaModelNativeCRIUID(t *testing.T) {
	uid := strings.Repeat("a1", 16)
	if verificationIdentity(uid) || secretshapes.Match(uid) == nil {
		t.Fatal("global credential/identity policy was weakened")
	}
	for _, tc := range []struct {
		name, uid, namespace, pod, state, refusal string
		retained                                  bool
	}{
		{"unrelated static Pod", uid, "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "", false},
		{"retained static UID", uid, "kube-system", "unrelated", "CONTAINER_RUNNING", "model credential process stop is unknown", true},
		{"gateway prefix", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_RUNNING", "model credential process stop is unknown", false},
		{"owned created", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_CREATED", "model credential process stop is unknown", false},
		{"owned unknown", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_UNKNOWN", "model credential process stop is unknown", false},
		{"owned exited", uid, OrkaNamespace, "copilot-vekil-other", "CONTAINER_EXITED", "", false},
		{"unrelated invalid state", uid, "kube-system", "etcd-test-control-plane", "INVALID", "model process state unavailable", false},
		{"empty UID", "", "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"control UID", "bad\nuid", "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"long UID", strings.Repeat("x", 257), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"key shaped UID", strings.Join([]string{"sk", "proj", strings.Repeat("X", 100)}, "-"), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"uppercase hash", strings.Repeat("AB", 16), "kube-system", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"bad namespace", uid, "bad.namespace", "etcd-test-control-plane", "CONTAINER_RUNNING", "model process identity unavailable", false},
		{"bad name", uid, "kube-system", "INVALID", "CONTAINER_RUNNING", "model process identity unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, roots := modelGatewayFixture(t)
			root, err := modelGatewayRoot(roots)
			if err != nil {
				t.Fatal(err)
			}
			modelSetCRI(t, modelCRIContainer(tc.uid, tc.namespace, tc.pod, tc.state))
			var children []modelChild
			if tc.retained {
				children = []modelChild{{Kind: "Pod", UID: uid}}
			}
			err = a.modelGatewayStopped(context.Background(), root, children)
			if tc.refusal == "" {
				if err != nil {
					t.Fatalf("valid native CRI metadata refused: %v", err)
				}
			} else if err == nil || err.Error() != tc.refusal {
				t.Fatalf("refusal=%v want %s", err, tc.refusal)
			}
		})
	}
}

func TestOrkaModelDisconnectWithStaticPod(t *testing.T) {
	a, objects, _ := modelLifecycleFixture(t)
	connected, err := a.ConnectOrkaModel(modelConnectOptions())
	if err != nil {
		t.Fatal(err)
	}
	modelSetCRI(t, modelCRIContainer(strings.Repeat("b2", 16), "kube-system", "kube-scheduler-test-control-plane", "CONTAINER_RUNNING"))
	modelDeleteFixture(a, objects)
	deletes := 0
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), " delete ") {
			deletes++
		}
		return capture(ctx, cmd)
	}
	result, err := a.DisconnectOrkaModel(OrkaModelDisconnectOptions{OperationID: "33333333-3333-4333-8333-333333333333", Namespace: OrkaNamespace, Provider: "copilot", Connection: connected.Connection.ID})
	if err != nil || result.Connection == nil || result.Connection.State != "gateway_stopped" || len(objects) != 0 {
		t.Fatalf("disconnect did not complete: %+v %v remaining%d", result, err, len(objects))
	}
	if deletes != 4 {
		t.Fatalf("DELETE count=%d want4", deletes)
	}
}
