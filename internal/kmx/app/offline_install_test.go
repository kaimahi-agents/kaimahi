package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"go.yaml.in/yaml/v3"
)

func offlineInstallFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir, m := offlineFixture(t)
	raw, err := os.ReadFile("testdata/offline-install.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Files       map[string]string
		ChartChunks []string
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for name, body := range f.Files {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var chart []byte
	for _, chunk := range f.ChartChunks {
		raw, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			t.Fatal(err)
		}
		chart = append(chart, raw...)
	}
	os.WriteFile(filepath.Join(dir, "chart.tgz"), chart, 0600)
	profileRaw, _ := os.ReadFile("offline_profiles/orka-59703d6e-native-v1.json")
	var p struct {
		Install struct {
			Chart     map[string]any
			Artifacts map[string]any
			Images    map[string]struct {
				Repository, IndexDigest string
				Platforms               map[string]struct{ ManifestDigest, ConfigDigest string }
			}
		}
	}
	json.Unmarshal(profileRaw, &p)
	m["chart"] = map[string]any{"path": "chart.tgz", "sha256": offlineTestHash(chart)}
	m["artifacts"] = map[string]any{"path": "images.json", "sha256": offlineTestHash([]byte(f.Files["images.json"]))}
	rows := []any{}
	for _, name := range strings.Fields("controller ai-worker general-worker publisher session-loader") {
		pin := p.Install.Images[name]
		child := pin.Platforms[runtime.GOARCH]
		prefix := name + "-" + runtime.GOARCH
		rows = append(rows, map[string]any{"component": name, "repository": pin.Repository, "indexPath": prefix + "-index.json", "indexDigest": pin.IndexDigest, "manifestPath": prefix + "-manifest.json", "manifestDigest": child.ManifestDigest, "configPath": prefix + "-config.json", "configDigest": child.ConfigDigest, "pullPolicy": "IfNotPresent"})
	}
	m["images"] = rows
	offlineWriteManifest(t, dir, m)
	return dir, m
}

func TestOfflineInstallBundle(t *testing.T) {
	dir, m := offlineInstallFixture(t)
	if _, err := loadOfflineInstallBundle(dir, offlineTestIdentity()); err != nil {
		t.Fatalf("complete actual catalog: %v", err)
	}
	for _, field := range []string{"chart", "artifacts"} {
		t.Run(field, func(t *testing.T) {
			row := m[field].(map[string]any)
			old := row["sha256"]
			row["sha256"] = strings.Repeat("f", 64)
			offlineWriteManifest(t, dir, m)
			if _, err := loadOfflineInstallBundle(dir, offlineTestIdentity()); err == nil {
				t.Fatal("unreviewed pin accepted")
			}
			row["sha256"] = old
		})
	}
}

// Every compiled custom-value field is required; even an otherwise harmless
// extra field is a different install and must never be treated as exact reuse.
func TestOfflineInstallOrphanPrivacy(t *testing.T) {
	a, dir, _, _ := offlineInstallApp(t)
	t.Setenv("OFFLINE_ORPHAN", "credential-must-not-echo.orka.ai")
	err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"})
	if err == nil || strings.Contains(err.Error(), "credential-must-not-echo") {
		t.Fatalf("orphan diagnostic exposed child output: %v", err)
	}
}
func TestOfflineInstallDryRunDoesNotCheckReadiness(t *testing.T) {
	a, dir, _, log := offlineInstallApp(t)
	t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":"deployed"}]`)
	path := os.Getenv("OFFLINE_LIVE")
	raw, _ := os.ReadFile(path)
	raw = bytes.ReplaceAll(raw, []byte(`"readyReplicas":1`), []byte(`"readyReplicas":0`))
	os.WriteFile(path, raw, 0600)
	if err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(log)
	if strings.Contains(string(commands), "rollout status") || strings.Contains(string(commands), "condition=Established") || strings.Contains(string(commands), "apply -f -") {
		t.Fatalf("dry run readiness/mutation: %s", commands)
	}
}
func TestOfflineInstallUnknownReleaseList(t *testing.T) {
	for _, raw := range []string{"null", "{}", "[{}]", "[] {}", "[{\"name\":\"orka\",\"name\":\"foreign\"}]"} {
		a, dir, _, log := offlineInstallApp(t)
		t.Setenv("OFFLINE_RELEASE", raw)
		if err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"}); err == nil {
			t.Fatalf("unknown release list accepted: %s", raw)
		}
		commands, _ := os.ReadFile(log)
		if strings.Contains(string(commands), "--server-side") || strings.Contains(string(commands), "helm install") {
			t.Fatalf("unknown release list caused writes: %s", commands)
		}
	}
}
func TestOfflineInstallEveryValueField(t *testing.T) {
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b.install.values, &doc)
	var walk func(map[string]any)
	walk = func(m map[string]any) {
		for key, value := range m {
			delete(m, key)
			raw, _ := json.Marshal(doc)
			if offlineSameValues(raw, b.install.values) {
				t.Fatalf("missing field accepted: %s", key)
			}
			m[key] = value
			if child, ok := value.(map[string]any); ok {
				walk(child)
			} else {
				m[key] = "changed"
				raw, _ = json.Marshal(doc)
				if offlineSameValues(raw, b.install.values) {
					t.Fatalf("changed field accepted: %s", key)
				}
				m[key] = value
			}
		}
	}
	walk(doc)
	if !offlineSameValues(b.install.values, b.install.values) {
		t.Fatal("exact values refused")
	}
}

func TestOfflineInstallPinsAndFiles(t *testing.T) {
	for _, component := range strings.Fields("controller ai-worker general-worker publisher session-loader") {
		for _, field := range []string{"repository", "indexDigest", "manifestDigest", "configDigest", "indexPath", "manifestPath", "configPath", "bytes", "symlink"} {
			t.Run(component+"/"+field, func(t *testing.T) {
				dir, m := offlineInstallFixture(t)
				for _, v := range m["images"].([]any) {
					row := v.(map[string]any)
					if row["component"] != component {
						continue
					}
					switch field {
					case "bytes":
						os.WriteFile(filepath.Join(dir, row["configPath"].(string)), []byte("{}"), 0600)
					case "symlink":
						p := filepath.Join(dir, row["configPath"].(string))
						os.Remove(p)
						os.Symlink("images.json", p)
					default:
						if strings.HasSuffix(field, "Digest") {
							row[field] = "sha256:" + strings.Repeat("f", 64)
						} else {
							row[field] = "unreviewed"
						}
					}
				}
				offlineWriteManifest(t, dir, m)
				if _, err := loadOfflineInstallBundle(dir, offlineTestIdentity()); err == nil {
					t.Fatal("changed OCI evidence accepted")
				}
			})
		}
	}
	for _, field := range []string{"chart", "artifacts"} {
		t.Run(field+" corrupt bytes", func(t *testing.T) {
			dir, m := offlineInstallFixture(t)
			path := m[field].(map[string]any)["path"].(string)
			os.WriteFile(filepath.Join(dir, path), []byte("{}"), 0600)
			if _, err := loadOfflineInstallBundle(dir, offlineTestIdentity()); err == nil {
				t.Fatal("changed source/chart accepted")
			}
		})
	}
}

func TestOfflineInstallNodeRefusals(t *testing.T) {
	for _, mode := range []string{"wrong node", "wrong kubeconfig", "missing context", "missing CRI", "kubernetes platform", "kubernetes names", "unschedulable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			a, dir, _, log := offlineInstallApp(t)
			switch mode {
			case "wrong node":
				t.Setenv("OFFLINE_NODE_IMAGE", "foreign")
			case "wrong kubeconfig":
				t.Setenv("OFFLINE_SELECTED_CLUSTER", `{"server":"https://127.0.0.1:7777"}`)
			case "missing context":
				t.Setenv("OFFLINE_SELECTED_CLUSTER", "{}")
			case "missing CRI":
				os.WriteFile(filepath.Join(filepath.Dir(log), "session-loader.cri"), []byte("{}"), 0600)
			case "kubernetes platform":
				t.Setenv("OFFLINE_KUBE_NODES", `{"items":[{"metadata":{"name":"test-control-plane"},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":"unknown"}}}]}`)
			case "kubernetes names":
				t.Setenv("OFFLINE_KUBE_NODES", `{"items":[{"metadata":{"name":"foreign"}}]}`)
			case "unschedulable":
				t.Setenv("OFFLINE_KUBE_NODES", fmt.Sprintf(`{"items":[{"metadata":{"name":"test-control-plane"},"spec":{"unschedulable":true},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":%q}}}]}`, runtime.GOARCH))
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				a = a.withRunContext(ctx)
			}
			err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"})
			if err == nil {
				t.Fatal("unproven target accepted")
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			raw, _ := os.ReadFile(log)
			for _, bad := range []string{"apply ", "helm list", "helm install", "export kubeconfig", "start "} {
				if strings.Contains(string(raw), bad) {
					t.Fatalf("preflight effect %q: %s", bad, raw)
				}
			}
		})
	}
}

func TestOfflineInstallValuesContract(t *testing.T) {
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/offline-values.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	json.Unmarshal(raw, &expected)
	expected["kmx"] = map[string]any{"offlineProfileSha256": offlineTestHash(offlineProfileJSON)}
	// The independent render fixture was audited on amd64; only the selected
	// child identities differ on arm64, never configuration or pull policies.
	imageValues := []map[string]any{
		expected["controller"].(map[string]any)["image"].(map[string]any),
		expected["workers"].(map[string]any)["ai"].(map[string]any)["image"].(map[string]any),
		expected["workers"].(map[string]any)["general"].(map[string]any)["image"].(map[string]any),
		expected["publisher"].(map[string]any)["image"].(map[string]any),
		expected["workers"].(map[string]any)["sessionLoader"].(map[string]any)["image"].(map[string]any),
	}
	for i, row := range b.install.images {
		imageValues[i]["digest"] = row.ManifestDigest
	}
	raw, _ = json.Marshal(expected)
	if !offlineSameValues(raw, b.install.values) {
		t.Fatal("complete generated values differ from independent fixed contract")
	}
}

func TestOfflineInstallEverySchedulableNode(t *testing.T) {
	a, _, b, log := offlineInstallApp(t)
	tools := filepath.Dir(log)
	raw, _ := os.ReadFile(filepath.Join(tools, "kind"))
	script := strings.Replace(string(raw), "\"get nodes\") echo test-control-plane ;;", "\"get nodes\") printf 'test-control-plane\\ntest-worker\\n' ;;", 1)
	os.WriteFile(filepath.Join(tools, "kind"), []byte(script), 0700)
	raw, _ = os.ReadFile(filepath.Join(tools, "docker"))
	script = strings.Replace(string(raw), "\"ps -a\") echo test-control-plane;;", "\"ps -a\") printf 'test-control-plane\\ntest-worker\\n';;", 1)
	script = strings.Replace(script, "\"exec test-control-plane\")", "\"exec test-control-plane\"|\"exec test-worker\")", 1)
	script = strings.Replace(script, `"Name":"/test-control-plane"`, `"Name":"/%s"`, 1)
	script = strings.Replace(script, `"$OFFLINE_NODE_IMAGE";;`, `"$OFFLINE_NODE_IMAGE" "$3";;`, 1)
	for _, r := range b.install.images {
		old := shellArg("exec test-control-plane crictl --runtime-endpoint unix:///run/containerd/containerd.sock --image-endpoint unix:///run/containerd/containerd.sock inspecti -o json " + r.Repository + "@" + r.ManifestDigest)
		script = strings.Replace(script, old+")", old+"|"+strings.Replace(old, "test-control-plane", "test-worker", 1)+")", 1)
	}
	os.WriteFile(filepath.Join(tools, "docker"), []byte(script), 0700)
	t.Setenv("OFFLINE_KUBE_NODES", fmt.Sprintf(`{"items":[{"metadata":{"name":"test-control-plane"},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":%q}}},{"metadata":{"name":"test-worker"},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":%q}}}]}`, runtime.GOARCH, runtime.GOARCH))
	if err := a.offlineInstallCache(b); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(log)
	if strings.Count(string(commands), "inspecti -o json") != 10 {
		t.Fatalf("did not inspect every image in every node: %s", commands)
	}
}
func TestOfflineInstallValuesCancellation(t *testing.T) {
	a, dir, _, log := offlineInstallApp(t)
	t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":"deployed"}]`)
	signal := filepath.Join(filepath.Dir(log), "values-started")
	t.Setenv("OFFLINE_VALUES_STARTED", signal)
	helm := filepath.Join(filepath.Dir(log), "helm")
	script, err := os.ReadFile(helm)
	if err != nil {
		t.Fatal(err)
	}
	script = bytes.Replace(script, []byte(`"get values") /bin/cat "$OFFLINE_VALUES";;`), []byte(`"get values") : > "$OFFLINE_VALUES_STARTED";exec /bin/sleep 60;;`), 1)
	if err := os.WriteFile(helm, script, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a = a.withRunContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(signal); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	err = a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"})
	<-done
	if _, err := os.Stat(signal); err != nil {
		t.Fatal("values-read cancellation barrier not reached")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("values read lost typed cancellation: %v", err)
	}
	commands, _ := os.ReadFile(log)
	for _, forbidden := range []string{"--server-side", "helm install", "rollout status", "apply -f -"} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("cancelled values read reached %q", forbidden)
		}
	}
}

func TestOfflineInstallActualHelm(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("guest supplied Helm unavailable")
	}
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	a := New(&config.Config{KubeContext: "kind-test"}).offlineHelmTarget()
	if err := a.renderOfflineChart(b.install); err != nil {
		t.Fatal(err)
	}
	// The verified chart's CRDs are established before install. Local lookup
	// cannot observe them, so the fixed values must render the same Gateway flag.
	args := b.install.workloads["orka-api-controller"].Containers[0].Args
	if !slices.Contains(args, "--gateway-enabled=true") {
		t.Fatal("local render differs from the post-CRD installed Gateway state")
	}
}
func TestOfflineInstallRenderRefusals(t *testing.T) {
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("testdata/offline-render.json")
	for _, mutate := range []func(string) string{
		func(s string) string {
			return strings.Replace(s, "--session-loader-image=", "--missing-loader-image=", 1)
		},
		func(s string) string { return strings.Replace(s, "IfNotPresent", "Always", 1) },
		func(s string) string {
			return strings.Replace(s, "--api-port=8080", "--acp-codex-runtime-image=foreign", 1)
		},
		func(s string) string { return strings.Replace(s, "--api-port=8080", "--acp-toolboxes-enabled=true", 1) },
		func(s string) string { return strings.Replace(s, "--api-port=8080", "-ai-worker-image=foreign", 1) },
		func(s string) string {
			return strings.Replace(s, `"containers": [`, `"ephemeralContainers":[{"image":"foreign"}],"containers": [`, 1)
		},
		func(s string) string {
			return strings.Replace(s, "ORKA_AI_WORKER_CLUSTER_ROLE_NAME", "ORKA_ACP_CODEX_RUNTIME_IMAGE", 1)
		},
		func(s string) string {
			return s + "\\n---\\nkind: Pod\\nmetadata: {name: surprise}\\nspec: {containers: [{image: foreign}]}"
		},
	} {
		if _, err := offlineRenderAudit([]byte(mutate(string(raw))), b.install.images); err == nil {
			t.Fatal("unsafe render accepted")
		}
	}
}
func TestOfflineInstallChangedReuse(t *testing.T) {
	for _, field := range []string{"values", "marker", "extra-values", "image", "policy", "command", "args", "env", "envFrom", "init", "extra-workload", "missing-workload", "unhealthy", "orphan"} {
		t.Run(field, func(t *testing.T) {
			a, dir, _, log := offlineInstallApp(t)
			if field != "orphan" {
				t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":"deployed"}]`)
			} else {
				t.Setenv("OFFLINE_ORPHAN", "tasks.core.orka.ai")
			}
			if field == "values" || field == "marker" || field == "extra-values" {
				path := os.Getenv("OFFLINE_VALUES")
				raw, _ := os.ReadFile(path)
				var doc map[string]any
				json.Unmarshal(raw, &doc)
				if field == "marker" {
					doc["kmx"] = map[string]any{"offlineProfileSha256": "foreign"}
				} else if field == "extra-values" {
					doc["extra"] = false
				} else {
					doc["providerProxy"] = map[string]any{"enabled": true}
				}
				raw, _ = json.Marshal(doc)
				os.WriteFile(path, raw, 0600)
			} else if field != "orphan" {
				path := os.Getenv("OFFLINE_LIVE")
				raw, _ := os.ReadFile(path)
				var doc map[string]any
				json.Unmarshal(raw, &doc)
				items := doc["items"].([]any)
				d := items[0].(map[string]any)
				spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				c := spec["containers"].([]any)[0].(map[string]any)
				switch field {
				case "image":
					c["image"] = "foreign"
				case "policy":
					c["imagePullPolicy"] = "Always"
				case "command":
					c["command"] = []any{"foreign"}
				case "args":
					c["args"] = []any{"foreign"}
				case "env":
					c["env"] = []any{map[string]any{"name": "ORKA_ACP_CODEX_RUNTIME_IMAGE", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "foreign", "key": "image"}}}}
				case "envFrom":
					c["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": "foreign"}}}
				case "init":
					spec["initContainers"] = []any{map[string]any{"name": "extra", "image": "foreign"}}
				case "extra-workload":
					doc["items"] = append(items, d)
				case "missing-workload":
					doc["items"] = items[:2]
				case "unhealthy":
					d["status"] = map[string]any{"readyReplicas": 1}
				}
				raw, _ = json.Marshal(doc)
				os.WriteFile(path, raw, 0600)
			}
			err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-"})
			if err == nil {
				t.Fatal("changed/orphan state adopted")
			}
			raw, _ := os.ReadFile(log)
			if strings.Contains(string(raw), "helm install") || strings.Contains(string(raw), "--server-side") || strings.Contains(string(raw), "apply -f -") {
				t.Fatalf("mutation after refusal: %s", raw)
			}
		})
	}
}
func TestOfflineInstallCRI(t *testing.T) {
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	row := b.install.images[0]
	valid := map[string]any{"status": map[string]any{"id": row.ConfigDigest, "repoDigests": []string{row.Repository + "@" + row.ManifestDigest}}, "info": map[string]any{"imageSpec": map[string]any{"os": "linux", "architecture": runtime.GOARCH, "rootfs": map[string]any{"type": "layers", "diff_ids": b.install.diffIDs[row.Component]}}}}
	raw, _ := json.Marshal(valid)
	if err := offlineCRIImage(raw, row, b.platform, b.install.diffIDs[row.Component]); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), row.ConfigDigest, "sha256:"+strings.Repeat("f", 64), 1),
		strings.Replace(string(raw), row.Repository+"@"+row.ManifestDigest, "import-123", 1),
		strings.Replace(string(raw), runtime.GOARCH, "unknown", 1),
		strings.Replace(string(raw), b.install.diffIDs[row.Component][0], "sha256:"+strings.Repeat("f", 64), 1),
		"{}", "null", string(raw) + " {}",
	} {
		if err := offlineCRIImage([]byte(bad), row, b.platform, b.install.diffIDs[row.Component]); err == nil {
			t.Fatalf("unproven CRI accepted: %s", bad)
		}
	}
}

func offlineRenderFixture(t *testing.T, b *offlineNodeBundle) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/offline-render.json")
	if err != nil {
		t.Fatal(err)
	}
	var p offlineProfile
	if err = json.Unmarshal(offlineProfileJSON, &p); err != nil {
		t.Fatal(err)
	}
	for _, row := range b.install.images {
		old := p.Install.Images[row.Component]
		raw = []byte(strings.ReplaceAll(string(raw), old.Repository+"@"+old.Platforms["amd64"].ManifestDigest, row.Repository+"@"+row.ManifestDigest))
	}
	return raw
}
func offlineInstallApp(t *testing.T) (*App, string, *offlineNodeBundle, string) {
	t.Helper()
	a, _, _, log := offlineClusterApp(t, "docker")
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(log)
	t.Setenv("OFFLINE_CONTEXT_MISSING", "")
	t.Setenv("OFFLINE_EXISTING", "test")
	raw := offlineRenderFixture(t, b)
	render := filepath.Join(tools, "render")
	os.WriteFile(render, raw, 0600)
	t.Setenv("OFFLINE_RENDER", render)
	values := filepath.Join(tools, "values")
	os.WriteFile(values, b.install.values, 0600)
	t.Setenv("OFFLINE_VALUES", values)
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	items := []any{}
	for {
		var obj map[string]any
		if dec.Decode(&obj) != nil {
			break
		}
		meta := obj["metadata"].(map[string]any)
		meta["uid"] = "owned"
		meta["generation"] = 1
		obj["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1}
		items = append(items, obj)
	}
	live := filepath.Join(tools, "live")
	raw, _ = json.Marshal(map[string]any{"items": items})
	os.WriteFile(live, raw, 0600)
	t.Setenv("OFFLINE_LIVE", live)
	nodes := fmt.Sprintf(`{"items":[{"metadata":{"name":"test-control-plane"},"spec":{},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":%q}}}]}`, runtime.GOARCH)
	t.Setenv("OFFLINE_KUBE_NODES", nodes)
	for _, key := range []string{"OFFLINE_PHASE_FAIL", "OFFLINE_RELEASE", "OFFLINE_ORPHAN"} {
		t.Setenv(key, "")
	}
	fakeTool(t, tools, "helm", `printf 'helm %s\\n' "$*" >> "$OFFLINE_LOG"
 case "$1 $2" in
 "version "*) ;;
 "template "*) /bin/cat >/dev/null; /bin/cat "$OFFLINE_RENDER";;
 "list "*) if [ -n "$OFFLINE_RELEASE" ]; then printf '%s' "$OFFLINE_RELEASE";else echo '[]';fi;;
 "get values") /bin/cat "$OFFLINE_VALUES";;
 "install orka") /bin/cat > "$OFFLINE_LOG.helm-values";[ "$OFFLINE_PHASE_FAIL" != helm ];;
 *) exit 98;;esac`)
	fakeTool(t, tools, "kubectl", `printf 'kubectl %s\\n' "$*" >> "$OFFLINE_LOG"
 case "$*" in
 "version --client") ;;
 "--context kind-test config view --raw --minify -o jsonpath={.clusters[0].cluster}") printf '%s' "$OFFLINE_SELECTED_CLUSTER";;
 "config view -o json") echo '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}';;
 "--context kind-test get nodes -o json") printf '%s' "$OFFLINE_KUBE_NODES";;
 "--context kind-test -n orka-system get deployment --ignore-not-found=true -o name") ;;
 "--context kind-test get crd -o jsonpath="*) if [ -n "$OFFLINE_ORPHAN" ];then printf '%s' "$OFFLINE_ORPHAN";fi;;
 "--context kind-test -n orka-system get deploy -o json") /bin/cat "$OFFLINE_LIVE";;
 "--context kind-test apply --server-side --field-manager=kmx "*) /bin/cat >/dev/null;[ "$OFFLINE_PHASE_FAIL" != crd ];;
 "--context kind-test wait --for=condition=Established --timeout=120s -f -") /bin/cat >/dev/null;[ "$OFFLINE_PHASE_FAIL" != established ];;
 "--context kind-test -n orka-system rollout status deployment/"*) [ "$OFFLINE_PHASE_FAIL" != rollout ];;
 "--context kind-test apply -f -") /bin/cat > "$OFFLINE_LOG.reader";[ "$OFFLINE_PHASE_FAIL" != reader ];;
 *) echo command-denied >&2;exit 98;;esac`)
	script := `printf 'engine %s\\n' "$*" >> "$OFFLINE_LOG"
 case "$1 $2" in
 "context inspect") printf '%s' "$OFFLINE_DOCKER_CONTEXT_JSON";;
 "info ") ;;
 "image inspect") /bin/cat "$OFFLINE_IMAGE_JSON";;
 "container inspect") printf '[{"Image":"%s","Name":"/test-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}}}]' "$OFFLINE_NODE_IMAGE";;
 "ps -a") echo test-control-plane;;
 "exec test-control-plane") case "$*" in
`
	for _, r := range b.install.images {
		ref := r.Repository + "@" + r.ManifestDigest
		response, _ := json.Marshal(map[string]any{"status": map[string]any{"id": r.ConfigDigest, "repoDigests": []string{ref}}, "info": map[string]any{"imageSpec": map[string]any{"os": "linux", "architecture": runtime.GOARCH, "rootfs": map[string]any{"type": "layers", "diff_ids": b.install.diffIDs[r.Component]}}}})
		path := filepath.Join(tools, r.Component+".cri")
		os.WriteFile(path, response, 0600)
		script += shellArg("exec test-control-plane crictl --runtime-endpoint unix:///run/containerd/containerd.sock --image-endpoint unix:///run/containerd/containerd.sock inspecti -o json "+ref) + ") /bin/cat " + shellArg(path) + ";;\n"
	}
	script += "*) exit 98;;esac;;\n*) exit 98;;esac"
	fakeTool(t, tools, "docker", script)
	return a, dir, b, log
}
func TestOfflineInstallTransaction(t *testing.T) {
	for _, mode := range []string{"install", "dry-run", "reuse", "foreign", "partial", "crd", "established", "helm", "rollout", "reader"} {
		t.Run(mode, func(t *testing.T) {
			a, dir, b, log := offlineInstallApp(t)
			if mode == "reuse" || mode == "partial" {
				status := "deployed"
				if mode == "partial" {
					status = "failed"
				}
				t.Setenv("OFFLINE_RELEASE", fmt.Sprintf(`[{"name":"orka","chart":"orka-0.0.0-dev","app_version":"v0.0.0-dev","status":%q}]`, status))
			}
			if mode == "foreign" {
				t.Setenv("OFFLINE_RELEASE", `[{"name":"orka","chart":"orka-0.3.0","app_version":"v0.3.0","status":"deployed"}]`)
			}
			if slices.Contains([]string{"crd", "established", "helm", "rollout", "reader"}, mode) {
				t.Setenv("OFFLINE_PHASE_FAIL", mode)
			}
			err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-", DryRun: mode == "dry-run", Model: "ignored", ModelURL: "https://unused.invalid"})
			success := slices.Contains([]string{"install", "dry-run", "reuse"}, mode)
			if (err == nil) != success {
				t.Fatalf("%s: %v", mode, err)
			}
			if !success {
				want := map[string]string{"foreign": "identity/status", "partial": "identity/status", "crd": "applying Orka CRD", "established": "waiting for Orka CRDs", "helm": "installing pinned Orka chart", "rollout": "rollout failed", "reader": "provisioning the Task result account"}[mode]
				if want == "" || !strings.Contains(err.Error(), want) {
					t.Fatalf("failure phase %s: want %q got %v", mode, want, err)
				}
			}
			raw, _ := os.ReadFile(log)
			commands := string(raw)
			if mode == "dry-run" {
				if !strings.Contains(commands, "--dry-run=server") {
					t.Fatal(commands)
				}
				for _, bad := range []string{"helm install", "condition=Established", "rollout status", "apply -f -"} {
					if strings.Contains(commands, bad) {
						t.Fatalf("dry run %s", commands)
					}
				}
			}
			if mode == "reuse" || mode == "foreign" || mode == "partial" {
				if strings.Contains(commands, "helm install") || strings.Contains(commands, "--server-side") {
					t.Fatalf("release overwritten: %s", commands)
				}
			}
			if mode == "install" {
				values, _ := os.ReadFile(log + ".helm-values")
				if !offlineSameValues(values, b.install.values) {
					t.Fatal("installed values differ from generated typed document")
				}
				if strings.Count(commands, "rollout status deployment/") != 3 {
					t.Fatal("not all three deployments checked")
				}
			}
			if !success && mode != "reader" {
				if _, e := os.Stat(log + ".reader"); e == nil {
					t.Fatal("result reader ran after failure")
				}
			}
			if mode == "crd" || mode == "established" {
				if strings.Contains(commands, "helm install") {
					t.Fatal("Helm ran after CRD failure")
				}
			}
		})
	}
}

func TestOfflineInstallLocalOnly(t *testing.T) {
	a, _, _, log := offlineClusterApp(t, "docker")
	dir, _ := offlineInstallFixture(t)
	b, err := loadOfflineInstallBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_TOOLCHAIN", "on")
	tools := filepath.Dir(log)
	fakeTool(t, tools, "helm", `printf 'helm %s|driver=%s|server=%s|tls=%s|sql=%s\\n' "$*" "$HELM_DRIVER" "${HELM_KUBEAPISERVER-unset}" "${HELM_KUBETLS_SERVER_NAME-unset}" "${HELM_DRIVER_SQL_CONNECTION_STRING-unset}" >> "$OFFLINE_LOG"
 if [ "${HELM_KUBECONTEXT-unset}" != unset ] || [ "${HELM_NAMESPACE-unset}" != unset ] || [ "${HELM_KUBECAFILE-unset}" != unset ] || [ "${HELM_KUBEINSECURE_SKIP_TLS_VERIFY-unset}" != unset ] || [ "${HELM_KUBETOKEN-unset}" != unset ] || [ "${HELM_KUBEASUSER-unset}" != unset ] || [ "${HELM_KUBEASGROUPS-unset}" != unset ];then exit 97;fi
 case "$1" in version) ;; template) /bin/cat >/dev/null; /bin/cat "$OFFLINE_RENDER";; *) exit 98;; esac`)
	raw := offlineRenderFixture(t, b)
	path := filepath.Join(tools, "render")
	os.WriteFile(path, raw, 0600)
	t.Setenv("OFFLINE_RENDER", path)
	for _, key := range strings.Fields("HELM_KUBEAPISERVER HELM_KUBETLS_SERVER_NAME HELM_DRIVER_SQL_CONNECTION_STRING HELM_KUBECONTEXT HELM_NAMESPACE HELM_KUBECAFILE HELM_KUBEINSECURE_SKIP_TLS_VERIFY HELM_KUBETOKEN HELM_KUBEASUSER HELM_KUBEASGROUPS") {
		t.Setenv(key, "hostile")
		a.Run.Env = append(a.Run.Env, key+"=copied-hostile")
	}
	t.Setenv("HELM_DRIVER", "sql")
	a.Run.Env = append(a.Run.Env, "HELM_DRIVER=sql")
	shared := append([]string(nil), a.Run.Env...)
	if err := a.OrkaInstall(OrkaOptions{OfflineBundle: dir, Provider: "-", NoApply: true, Model: "ignored", ModelURL: "https://unused.invalid"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(log)
	commands := string(raw)
	for _, bad := range []string{"config view", "get nodes", "exec ", "apply ", "install ", "hostile"} {
		if strings.Contains(commands, bad) {
			t.Fatalf("forbidden %q in %s", bad, commands)
		}
	}
	if !strings.Contains(commands, "driver=secret|server=unset|tls=unset|sql=unset") {
		t.Fatalf("Helm binding: %s", commands)
	}
	if !reflect.DeepEqual(shared, a.Run.Env) {
		t.Fatal("shared runner mutated")
	}
}

func TestOfflineInstallEarlyRefusal(t *testing.T) {
	for _, tc := range []struct {
		opt  OrkaOptions
		want string
	}{
		{OrkaOptions{OfflineBundle: "missing"}, "--provider -"},
		{OrkaOptions{OfflineBundle: "missing", Provider: " - "}, "--provider -"},
		{OrkaOptions{OfflineBundle: " ", Provider: "-"}, "non-empty"},
		{OrkaOptions{OfflineBundle: "missing", Provider: "-", NoApply: true, DryRun: true}, "cannot be used together"},
	} {
		a, _, _, log := offlineClusterApp(t, "docker")
		err := a.OrkaInstall(tc.opt)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q got %v", tc.want, err)
		}
		raw, _ := os.ReadFile(log)
		if strings.Contains(string(raw), "apply") || strings.Contains(string(raw), "install") {
			t.Fatalf("unexpected effects: %s", raw)
		}
	}
}
