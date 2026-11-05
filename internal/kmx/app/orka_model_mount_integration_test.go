package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func modelMountFixture(t *testing.T) (*App, string, string) {
	t.Helper()
	a, dir, _, log := offlineClusterApp(t, "docker")
	tools := filepath.Dir(log)
	for key, value := range map[string]string{
		"MODEL_SOURCE": "tmpfs rw,nosuid,nodev,noexec", "MODEL_DIRECTORY": "directory:0:65532:750:17:2", "MODEL_PARENT": "directory:0:0:755:1:1",
		"MODEL_LIVE": "directory:0:65532:750:17:2", "MODEL_NODE_FS": "tmpfs ro,nosuid,nodev,noexec", "MODEL_MOUNT_RW": "false", "MODEL_PROPAGATION": "rprivate", "MODEL_RUNNING": "true",
		"MODEL_MOUNT_SOURCE": modelCredentialDir, "MODEL_NODE_FAIL": "", "MODEL_NESTED": "",
	} {
		t.Setenv(key, value)
	}
	fakeTool(t, tools, "findmnt", `printf 'source findmnt %s\n' "$*" >> "$OFFLINE_LOG"; printf '%s\n' "$MODEL_SOURCE"`)
	fakeTool(t, tools, "stat", `printf 'source stat %s\n' "$*" >> "$OFFLINE_LOG"; case "$*" in *credentials) printf '%s\n' "$MODEL_DIRECTORY";; *) printf '%s\n' "$MODEL_PARENT";; esac`)
	fakeTool(t, tools, "docker", `printf 'engine %s\n' "$*" >> "$OFFLINE_LOG"
case "$1 $2" in
"context inspect") printf '%s' "$OFFLINE_DOCKER_CONTEXT_JSON";;
"version "|"info ") ;;
"image inspect") /bin/cat "$OFFLINE_IMAGE_JSON";;
"ps -a") if [ -n "$OFFLINE_INVENTORY_BAD" ]; then echo other-control-plane; else echo test-control-plane; fi;;
"container inspect")
 if [ -n "$MODEL_NODE_FAIL" ]; then exit 1; fi
 printf '[{"Image":"%s","Name":"/test-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}},"State":{"Running":%s},"Mounts":[{"Type":"bind","Source":"%s","Destination":"/run/bridge/credentials","RW":%s,"Propagation":"%s"}%s]}]' "$OFFLINE_NODE_IMAGE" "$MODEL_RUNNING" "$MODEL_MOUNT_SOURCE" "$MODEL_MOUNT_RW" "$MODEL_PROPAGATION" "$MODEL_NESTED";;
"exec test-control-plane") case "$3" in stat) printf '%s\n' "$MODEL_LIVE";; findmnt) printf '%s\n' "$MODEL_NODE_FS";; *) exit 98;; esac;;
*) exit 98;; esac`)
	return a, dir, log
}

func TestCredentialMountUp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixed credential mount is guest Linux only")
	}
	for _, offline := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("offline%v-existing%v", offline, existing), func(t *testing.T) {
				a, dir, log := modelMountFixture(t)
				if existing {
					t.Setenv("OFFLINE_EXISTING", "test")
				}
				opt := UpOptions{Step: "cluster", CredentialMount: "copilot-v1"}
				if offline {
					opt.OfflineBundle = dir
				}
				if err := a.UpWithOptions(opt); err != nil {
					t.Fatal(err)
				}
				calls, _ := os.ReadFile(log)
				if existing {
					if !strings.Contains(string(calls), "exec test-control-plane stat") {
						t.Fatal("running node identity not checked")
					}
					for _, bad := range []string{"kind create", "engine start"} {
						if strings.Contains(string(calls), bad) {
							t.Fatal("existing node mutated")
						}
					}
				} else {
					raw, err := os.ReadFile(log + ".stdin")
					if err != nil {
						t.Fatal(err)
					}
					want, _ := modelKindConfig("copilot-v1")
					if string(raw) != string(want) {
						t.Fatalf("wrong first-creation config %s", raw)
					}
					create := strings.Index(string(calls), "kind create")
					inspect := strings.LastIndex(string(calls), "container inspect")
					if create < 0 || inspect < create {
						t.Fatal("created node mount was not checked")
					}
				}
				if a.modelCredentialMount != "" {
					t.Fatal("profile escaped operation-local App")
				}
			})
		}
	}
}

func TestCredentialMountRefusalBeforeEffects(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("guest Linux only")
	}
	for _, offline := range []bool{false, true} {
		for _, damage := range []string{"plain-directory", "missing-flag", "owner", "group", "mode", "link", "parent", "writable", "wrong-source", "propagation", "nested", "identity", "live-type", "live-rw", "inventory", "unreadable", "foreign-context"} {
			t.Run(fmt.Sprintf("offline%v-%s", offline, damage), func(t *testing.T) {
				a, dir, log := modelMountFixture(t)
				t.Setenv("OFFLINE_EXISTING", "test")
				switch damage {
				case "plain-directory":
					t.Setenv("MODEL_SOURCE", "ext4 rw,nosuid,nodev,noexec")
				case "missing-flag":
					t.Setenv("MODEL_SOURCE", "tmpfs rw,nosuid,nodev")
				case "owner":
					t.Setenv("MODEL_DIRECTORY", "directory:1:65532:750:17:2")
				case "group":
					t.Setenv("MODEL_DIRECTORY", "directory:0:0:750:17:2")
				case "mode":
					t.Setenv("MODEL_DIRECTORY", "directory:0:65532:777:17:2")
				case "link":
					t.Setenv("MODEL_DIRECTORY", "symbolic link:0:65532:750:17:2")
				case "parent":
					t.Setenv("MODEL_PARENT", "directory:0:0:777:1:1")
				case "writable":
					t.Setenv("MODEL_MOUNT_RW", "true")
				case "wrong-source":
					t.Setenv("MODEL_MOUNT_SOURCE", "/persistent/credentials")
				case "propagation":
					t.Setenv("MODEL_PROPAGATION", "rshared")
				case "nested":
					t.Setenv("MODEL_NESTED", `,{"Type":"bind","Source":"/persistent/token","Destination":"/run/bridge/credentials/token","RW":true,"Propagation":"rprivate"}`)
				case "identity":
					t.Setenv("MODEL_LIVE", "directory:0:65532:750:22:2")
				case "live-type":
					t.Setenv("MODEL_NODE_FS", "ext4 ro,nosuid,nodev,noexec")
				case "live-rw":
					t.Setenv("MODEL_NODE_FS", "tmpfs rw,nosuid,nodev,noexec")
				case "inventory":
					t.Setenv("OFFLINE_INVENTORY_BAD", "1")
				case "unreadable":
					t.Setenv("MODEL_NODE_FAIL", "1")
				case "foreign-context":
					t.Setenv("OFFLINE_CONTEXT_MISSING", "")
					t.Setenv("OFFLINE_SELECTED_CLUSTER", `{"server":"https://foreign.invalid"}`)
				}
				opt := UpOptions{Step: "cluster", CredentialMount: "copilot-v1"}
				if offline {
					opt.OfflineBundle = dir
				}
				if err := a.UpWithOptions(opt); err == nil {
					t.Fatal("accepted incompatible credential mount")
				}
				calls, _ := os.ReadFile(log)
				for _, bad := range []string{"kind create", "engine start", "export kubeconfig", "get --raw=/readyz", "rollout status"} {
					if strings.Contains(string(calls), bad) {
						t.Fatalf("unsafe effect %s\n%s", bad, calls)
					}
				}
			})
		}
	}
}

func TestCredentialMountStoppedNode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("guest Linux only")
	}
	a, _, _ := modelMountFixture(t)
	t.Setenv("MODEL_RUNNING", "false")
	if err := a.checkModelNodeMounts(); err != nil {
		t.Fatal(err)
	}
}

func TestModelCaptureOnlyPlaceholderSecret(t *testing.T) {
	// Desired credential values must be rejected before attempting any API send.
	doc := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "model-key", "namespace": OrkaNamespace, "annotations": map[string]any{modelOperationAnnotation: "11111111-1111-4111-8111-111111111111"}}, "type": "Opaque", "data": map[string]any{"api-key": "arbitrary-base64"}}
	raw, _ := json.Marshal(doc)
	var normalized map[string]any
	json.Unmarshal(raw, &normalized)
	if _, err := modelDesired(normalized); err == nil {
		t.Fatal("arbitrary Secret accepted")
	}
}
