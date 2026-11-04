package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func offlineImageFixture(t *testing.T, b *offlineNodeBundle, id string) string {
	t.Helper()
	raw, err := json.Marshal([]any{map[string]any{"Id": id, "Os": b.platform.OS, "Architecture": b.platform.Architecture, "RootFS": map[string]any{"Type": "layers", "Layers": b.diffIDs}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOfflineEngineIdentity(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, kind := range []string{"config", "manifest", "index", "bare config", "bare manifest", "bare index", "foreign", "platform", "variant", "rootfs", "malformed", "null", "multiple", "missing", "oversize", "duplicate"} {
			t.Run(engine+"/"+kind, func(t *testing.T) {
				dir, _ := offlineFixture(t)
				b, err := loadOfflineNodeBundle(dir, offlineTestIdentity())
				if err != nil {
					t.Fatal(err)
				}
				id := b.node.ConfigDigest
				if kind == "manifest" || kind == "bare manifest" {
					id = b.node.ManifestDigest
				}
				if kind == "index" || kind == "bare index" {
					id = *b.node.IndexDigest
				}
				if strings.HasPrefix(kind, "bare ") {
					id = strings.TrimPrefix(id, "sha256:")
				}
				if kind == "foreign" {
					id = "sha256:" + strings.Repeat("f", 64)
				}
				raw := offlineImageFixture(t, b, id)
				switch kind {
				case "platform":
					raw = strings.ReplaceAll(raw, `"Os":"linux"`, `"Os":"darwin"`)
				case "variant":
					raw = strings.ReplaceAll(raw, `"Os":"linux"`, `"Os":"linux","Variant":"v7"`)
				case "rootfs":
					raw = strings.ReplaceAll(raw, b.diffIDs[0], "sha256:"+strings.Repeat("e", 64))
				case "malformed":
					raw = "credential-must-not-echo"
				case "null":
					raw = "null"
				case "multiple":
					raw = "[{},{}]"
				case "oversize":
					raw = strings.Repeat(" ", offlineJSONLimit+1)
				case "duplicate":
					raw = `[{"Id":"` + id + `","Id":"` + id + `"}]`
				}
				tools := t.TempDir()
				input := filepath.Join(tools, "image.json")
				os.WriteFile(input, []byte(raw), 0600)
				t.Setenv("OFFLINE_IMAGE_JSON", input)
				t.Setenv("OFFLINE_TEST_KIND", kind)
				fakeTool(t, tools, engine, `if [ "$OFFLINE_TEST_KIND" = missing ]; then echo credential-must-not-echo >&2; exit 1; fi
case "$1 $2" in "image inspect") /bin/cat "$OFFLINE_IMAGE_JSON";; *) exit 98;; esac`)
				t.Setenv("PATH", tools)
				a := New(&config.Config{ContainerEngine: engine})
				var out bytes.Buffer
				a.Out, a.Err, a.Run.Stdout, a.Run.Stderr = &out, &out, &out, &out
				err = a.verifyOfflineNodeImage(b, b.node.LocalRef)
				valid := kind == "config" || kind == "manifest" || kind == "index" || strings.HasPrefix(kind, "bare ")
				if (err == nil) != valid {
					t.Fatalf("identity %s: %v", kind, err)
				}
				if !valid {
					want := "does not match pinned"
					if kind == "malformed" || kind == "null" || kind == "multiple" || kind == "duplicate" {
						want = "malformed/unknown engine inspection"
					}
					if kind == "missing" || kind == "oversize" {
						want = "local inspection failed"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("want %q, got %v", want, err)
					}
				}
				if err != nil && strings.Contains(err.Error(), "credential-must-not-echo") {
					t.Fatal("untrusted subprocess body leaked")
				}
			})
		}
	}
}

func offlineNoDownloads(t *testing.T) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	old := toolchainBase
	toolchainBase = server.URL
	t.Cleanup(func() {
		toolchainBase = old
		server.Close()
		if n := requests.Load(); n != 0 {
			t.Errorf("offline attempted %d HTTP downloads", n)
		}
	})
	t.Setenv("KMX_TOOLCHAIN", "")
	if err := os.Unsetenv("KMX_TOOLCHAIN"); err != nil {
		t.Fatal(err)
	}
}

func offlineClusterApp(t *testing.T, engine string) (*App, string, *offlineNodeBundle, string) {
	t.Helper()
	offlineNoDownloads(t)
	dir, _ := offlineFixture(t)
	b, err := loadOfflineNodeBundle(dir, offlineTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	log := filepath.Join(tools, "commands")
	input := filepath.Join(tools, "image.json")
	os.WriteFile(input, []byte(offlineImageFixture(t, b, b.node.ConfigDigest)), 0600)
	t.Setenv("OFFLINE_IMAGE_JSON", input)
	t.Setenv("OFFLINE_LOG", log)
	t.Setenv("OFFLINE_NODE_IMAGE", b.node.ConfigDigest)
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("KUBECONFIG", filepath.Join(tools, "selected-kubeconfig"))
	for _, name := range []string{"OFFLINE_EXISTING", "OFFLINE_KIND_FAIL", "OFFLINE_CREATE_FAIL", "OFFLINE_NODE_BAD", "OFFLINE_INVENTORY_BAD", "OFFLINE_CONTEXT_MISSING", "OFFLINE_ENGINE_FAIL", "OFFLINE_BLOCK_IMAGE"} {
		t.Setenv(name, "")
	}
	fakeTool(t, tools, "kind", `printf 'kind %s|kube=%s|provider=%s\n' "$*" "$KUBECONFIG" "${KIND_EXPERIMENTAL_PROVIDER-unset}" >> "$OFFLINE_LOG"
case "$1 $2" in
"version ") ;;
"get clusters") if [ -n "$OFFLINE_KIND_FAIL" ]; then exit 1; fi; printf '%s' "$OFFLINE_EXISTING" ;;
"get nodes") echo test-control-plane ;;
"create cluster") /bin/cat > "$OFFLINE_LOG.stdin"; if [ -n "$OFFLINE_CREATE_FAIL" ]; then exit 1; fi ;;
"export kubeconfig") ;;
*) exit 98;; esac`)
	fakeTool(t, tools, "kubectl", `printf 'kubectl %s|kube=%s\n' "$*" "$KUBECONFIG" >> "$OFFLINE_LOG"
case "$*" in
"version --client") ;;
*"config view"*) if [ -n "$OFFLINE_CONTEXT_MISSING" ]; then echo '{"contexts":[],"clusters":[]}'; else echo '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}'; fi ;;
"--context kind-test get --raw=/readyz"|"--context kind-test -n kube-system rollout status deployment/coredns --timeout=180s") ;;
*) exit 98;; esac`)
	fakeTool(t, tools, engine, `printf 'engine %s|kube=%s\n' "$*" "$KUBECONFIG" >> "$OFFLINE_LOG"
case "$1 $2" in
"info ") if [ -n "$OFFLINE_ENGINE_FAIL" ]; then exit 1; fi ;;
"image inspect") if [ -n "$OFFLINE_BLOCK_IMAGE" ]; then exec /bin/sleep 30; fi; /bin/cat "$OFFLINE_IMAGE_JSON" ;;
"container inspect") if [ -n "$OFFLINE_NODE_BAD" ]; then echo '[{"Image":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","Name":"/test-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}}}]'; else printf '[{"Image":"%s","Name":"/test-control-plane","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}}}]' "$OFFLINE_NODE_IMAGE"; fi ;;
"ps -a") if [ -n "$OFFLINE_INVENTORY_BAD" ]; then echo other-control-plane; else echo test-control-plane; fi ;;
"start test-control-plane") ;;
*) exit 98;; esac`)
	t.Setenv("PATH", tools)
	a := New(&config.Config{KindCluster: "test", KubeContext: "kind-test", ContextSource: config.SourceKubeCtx, ContainerEngine: engine})
	var out bytes.Buffer
	a.Out, a.Err, a.Run.Stdout, a.Run.Stderr = &out, &out, &out, &out
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closed.Close() })
	a.Stdin = closed
	a.offlineIdentity = func() (offlineKMXIdentity, error) { return offlineTestIdentity(), nil }
	return a, dir, b, log
}

// The fake executables stand in for the external engine/API only. The real
// bundle loader, local probes, context guard, Up orchestration and kind handlers
// run, so a wrong command/target or accidental model/provisioning phase fails.
func TestOfflineClusterUp(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, mode := range []string{"create", "reuse", "export", "unknown inventory", "wrong cache", "wrong node", "inventory disagreement", "failed create", "missing tool", "engine unavailable", "cancelled", "target disagreement", "invalid bundle"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				a, dir, b, log := offlineClusterApp(t, engine)
				switch mode {
				case "reuse", "export", "wrong node", "inventory disagreement":
					t.Setenv("OFFLINE_EXISTING", "test\n")
				}
				switch mode {
				case "export", "wrong node", "inventory disagreement":
					t.Setenv("OFFLINE_CONTEXT_MISSING", "1")
				case "unknown inventory":
					t.Setenv("OFFLINE_KIND_FAIL", "1")
				case "wrong cache":
					os.WriteFile(os.Getenv("OFFLINE_IMAGE_JSON"), []byte(offlineImageFixture(t, b, "sha256:"+strings.Repeat("f", 64))), 0600)
				case "failed create":
					t.Setenv("OFFLINE_CREATE_FAIL", "1")
				case "missing tool":
					os.Remove(filepath.Join(os.Getenv("PATH"), "kind"))
				case "engine unavailable":
					t.Setenv("OFFLINE_ENGINE_FAIL", "1")
				case "cancelled":
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					a = a.withRunContext(ctx)
				case "target disagreement":
					a.Cfg.KubeContext = "kind-other"
				case "invalid bundle":
					if err := os.WriteFile(filepath.Join(dir, "install.json"), []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "wrong node" {
					t.Setenv("OFFLINE_NODE_BAD", "1")
				}
				if mode == "inventory disagreement" {
					t.Setenv("OFFLINE_INVENTORY_BAD", "1")
				}
				err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				success := mode == "create" || mode == "reuse" || mode == "export"
				if (err == nil) != success {
					t.Fatalf("mode=%s: %v", mode, err)
				}
				if !success {
					want := map[string]string{
						"unknown inventory": "refusing to guess", "wrong cache": "does not match pinned",
						"wrong node": "mismatched immutable image/cluster identity", "inventory disagreement": "inventories disagree",
						"failed create": "effects are retained", "missing tool": "kind is not on PATH",
						"engine unavailable": engine + " was found", "cancelled": "context canceled",
						"target disagreement": "does not identify kind cluster", "invalid bundle": "install.json: JSON fields must have the complete closed shape",
					}[mode]
					if want == "" || !strings.Contains(err.Error(), want) {
						t.Fatalf("want %q, got %v", want, err)
					}
				}
				raw, _ := os.ReadFile(log)
				calls := string(raw)
				if strings.Contains(calls, "pull") || strings.Contains(calls, "helm") || strings.Contains(calls, "ollama") || strings.Contains(calls, "apply") || strings.Contains(calls, "delete") || strings.Contains(calls, "use-context") {
					t.Fatalf("unintended phase:\n%s", calls)
				}
				if got := os.Getenv("KMX_TOOLCHAIN"); got != "" {
					t.Fatalf("mutated toolchain env: %q", got)
				}
				if len(a.provisioned) != 0 {
					t.Fatal("offline provisioned tools")
				}
				if success {
					if !strings.Contains(calls, "engine image inspect "+b.node.LocalRef) {
						t.Fatalf("no cached-tag check:\n%s", calls)
					}
					for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
						if !strings.Contains(line, "|kube="+os.Getenv("KUBECONFIG")) {
							t.Fatalf("lost selected kubeconfig: %s", line)
						}
					}
					for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
						if strings.HasPrefix(line, "kind ") && !strings.HasSuffix(line, "|provider="+engine) {
							t.Fatalf("wrong offline kind provider: %s", line)
						}
					}
					if !strings.Contains(calls, "kubectl --context kind-test get --raw=/readyz") {
						t.Fatalf("no explicit target readiness:\n%s", calls)
					}
				}
				created := strings.Contains(calls, "kind create cluster")
				if created != (mode == "create" || mode == "failed create") {
					t.Fatalf("unexpected creation mode=%s:\n%s", mode, calls)
				}
				if mode == "create" {
					if !strings.Contains(calls, "kind create cluster --name test --config - --image "+b.node.LocalRef) {
						t.Fatalf("missing explicit image:\n%s", calls)
					}
					input, _ := os.ReadFile(log + ".stdin")
					if !bytes.Contains(input, []byte("syncFrequency: 5s")) {
						t.Fatal("changed kubelet config")
					}
				}
				if mode == "failed create" && (err == nil || !strings.Contains(err.Error(), "retained") || !strings.Contains(err.Error(), "test")) {
					t.Fatalf("missing retained effects: %v", err)
				}
				export := strings.Contains(calls, "kind export kubeconfig --name test")
				if export != (mode == "export") {
					t.Fatalf("unexpected kubeconfig export mode=%s:\n%s", mode, calls)
				}
				start := strings.Contains(calls, "engine start")
				if start != (engine == "podman" && (mode == "reuse" || mode == "export")) {
					t.Fatalf("unexpected start mode=%s:\n%s", mode, calls)
				}
				if mode == "reuse" || mode == "export" {
					if strings.Count(calls, "engine ps -a") != 1 {
						t.Fatalf("re-read rather than use verified node inventory: %s", calls)
					}
					inspect := strings.Index(calls, "engine container inspect test-control-plane")
					later := strings.Index(calls, "kubectl --context kind-test get --raw=/readyz")
					if inspect < 0 || later < inspect || !strings.Contains(calls, "engine image inspect "+b.node.ConfigDigest) {
						t.Fatalf("reuse not bound to immutable image before readiness:\n%s", calls)
					}
				}
				if (mode == "target disagreement" || mode == "invalid bundle" || mode == "cancelled") && calls != "" {
					t.Fatalf("probed before target refusal: %s", calls)
				}
			})
		}
	}
}

// A parent index inspection can select the host child, not the child actually
// used by a container. Removing container-specific binding must fail this test.
func TestOfflineContainerSelectedChild(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, mode := range []string{"index expected", "index foreign", "index missing", "index unknown", "index null", "bare index expected", "bare index missing", "bare index foreign", "config missing", "manifest missing", "config foreign"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				a, dir, b, log := offlineClusterApp(t, engine)
				t.Setenv("OFFLINE_EXISTING", "test\n")
				t.Setenv("OFFLINE_CONTEXT_MISSING", "1")
				id := *b.node.IndexDigest
				if strings.HasPrefix(mode, "bare") {
					id = strings.TrimPrefix(id, "sha256:")
				} else if strings.HasPrefix(mode, "config") {
					id = b.node.ConfigDigest
				} else if strings.HasPrefix(mode, "manifest") {
					id = b.node.ManifestDigest
				}
				row := map[string]any{"Image": id, "Name": "/test-control-plane", "Config": map[string]any{"Labels": map[string]string{"io.x-k8s.kind.cluster": "test"}}}
				child := b.node.ManifestDigest
				if strings.HasSuffix(mode, "foreign") {
					// The other real child of the same compiled parent index.
					child = "sha256:cf1d8a817f85226f8b256fca15fb0eebcb82ebc8598f496b9f9290f6cffb4688"
					if b.platform.Architecture == "arm64" {
						child = "sha256:986401fce0e567a9860075ba9df8c8459bd99a35f79024d8a682773fb40f0753"
					}
				}
				if !strings.HasSuffix(mode, "missing") {
					row["ImageManifestDescriptor"] = map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": child, "size": 1370}
				}
				if mode == "index unknown" {
					row["ImageManifestDescriptor"] = map[string]any{}
				} else if mode == "index null" {
					row["ImageManifestDescriptor"] = nil
				}
				raw, err := json.Marshal([]any{row})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("OFFLINE_CONTAINER_JSON", string(raw))
				fakeTool(t, filepath.Dir(log), engine, `printf 'engine %s|kube=%s\n' "$*" "$KUBECONFIG" >> "$OFFLINE_LOG"
case "$1 $2" in
"info ") ;;
"image inspect") /bin/cat "$OFFLINE_IMAGE_JSON" ;;
"container inspect") printf '%s' "$OFFLINE_CONTAINER_JSON" ;;
"ps -a") echo test-control-plane ;;
"start test-control-plane") ;;
*) exit 98;; esac`)
				// Simulate an index-ID store reporting native metadata regardless of
				// the container's actual selected manifest.
				if err := os.WriteFile(os.Getenv("OFFLINE_IMAGE_JSON"), []byte(offlineImageFixture(t, b, *b.node.IndexDigest)), 0600); err != nil {
					t.Fatal(err)
				}
				err = a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				calls, _ := os.ReadFile(log)
				valid := mode == "index expected" || mode == "bare index expected" || mode == "config missing" || mode == "manifest missing"
				if valid {
					if err != nil || !bytes.Contains(calls, []byte("kind export kubeconfig")) {
						t.Fatalf("valid container refused: %v\n%s", err, calls)
					}
					if strings.HasSuffix(mode, "expected") && !bytes.Contains(calls, []byte("engine image inspect "+b.node.ManifestDigest)) {
						t.Fatalf("did not inspect the container-selected child:\n%s", calls)
					}
				} else if err == nil || !strings.Contains(err.Error(), "selected manifest") || bytes.Contains(calls, []byte("kind export kubeconfig")) || bytes.Contains(calls, []byte("engine start")) {
					t.Fatalf("unbound child crossed export/start boundary: %v\n%s", err, calls)
				}
			})
		}
	}
}

// Returning to Runner.Capture would leak probe diagnostics and accept oversized
// stdout; the real Up path must refuse before any image or cluster operation.
func TestOfflineProbeOutputSafety(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, tool := range []string{"kind", "kubectl", engine} {
			for _, mode := range []string{"stderr", "oversized stderr", "oversized stdout"} {
				t.Run(engine+"/"+tool+"/"+mode, func(t *testing.T) {
					a, dir, _, log := offlineClusterApp(t, engine)
					payload := "credential-probe-canary=synthetic\x1b[31m\r\n"
					if mode != "stderr" {
						payload += strings.Repeat("x", offlineJSONLimit+1)
					}
					input := filepath.Join(t.TempDir(), "probe-output")
					if err := os.WriteFile(input, []byte(payload), 0600); err != nil {
						t.Fatal(err)
					}
					label := tool
					if tool == engine {
						label = "engine"
					}
					redirect, status := " >&2", "1"
					if mode == "oversized stdout" {
						redirect, status = "", "0"
					}
					fakeTool(t, os.Getenv("PATH"), tool, `printf '%s %s\n' `+shellArg(label)+` "$*" >> "$OFFLINE_LOG"
/bin/cat `+shellArg(input)+redirect+`
exit `+status)
					err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
					if err == nil {
						t.Fatal("accepted unusable or oversized probe output")
					}
					output := a.Out.(*bytes.Buffer).String() + err.Error()
					if len(output) > 4096 || strings.Contains(output, "credential-probe-canary") || strings.ContainsAny(output, "\x1b\r") {
						t.Fatalf("probe diagnostics leaked or were unbounded (%d bytes)", len(output))
					}
					if !strings.Contains(err.Error(), tool) {
						t.Fatalf("missing safe component diagnostic: %v", err)
					}
					raw, err := os.ReadFile(log)
					if err != nil {
						t.Fatal(err)
					}
					var calls []string
					for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
						calls = append(calls, strings.SplitN(line, "|", 2)[0])
					}
					if got := strings.Join(calls, "\n"); got != "kind version\nkubectl version --client\nengine info" {
						t.Fatalf("probe order changed or continued beyond preflight:\n%s", got)
					}
				})
			}
		}
	}
}

// Formatting probe failures into an aggregate string must not erase context
// identity or allow commands after the active cancelled/deadline-bound probe.
func TestOfflineCancellationDuringProbes(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for stage, tool := range []string{"kind", "kubectl", engine} {
			for _, deadline := range []bool{false, true} {
				name := "cancelled"
				if deadline {
					name = "deadline"
				}
				t.Run(engine+"/"+tool+"/"+name, func(t *testing.T) {
					a, dir, _, log := offlineClusterApp(t, engine)
					label := tool
					if tool == engine {
						label = "engine"
					}
					fakeTool(t, os.Getenv("PATH"), tool, `printf '%s %s\n' `+shellArg(label)+` "$*" >> "$OFFLINE_LOG"
exec /bin/sleep 30`)
					limit, wantErr := 5*time.Second, context.Canceled
					if deadline {
						limit, wantErr = 250*time.Millisecond, context.DeadlineExceeded
					}
					ctx, cancel := context.WithTimeout(context.Background(), limit)
					defer cancel()
					a = a.withRunContext(ctx)
					probes := []string{"kind version", "kubectl version --client", "engine info"}
					observed := make(chan bool, 1)
					go func() {
						tick := time.NewTicker(time.Millisecond)
						defer tick.Stop()
						for {
							select {
							case <-ctx.Done():
								observed <- false
								return
							case <-tick.C:
								raw, _ := os.ReadFile(log)
								if strings.Contains(string(raw), probes[stage]) {
									observed <- true
									if !deadline {
										cancel()
									}
									return
								}
							}
						}
					}()
					err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
					if !<-observed || !errors.Is(err, wantErr) {
						t.Fatalf("active probe did not preserve %v: %v", wantErr, err)
					}
					raw, err := os.ReadFile(log)
					if err != nil {
						t.Fatal(err)
					}
					var calls []string
					for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
						calls = append(calls, strings.SplitN(line, "|", 2)[0])
					}
					if got := strings.Join(calls, "\n"); got != strings.Join(probes[:stage+1], "\n") {
						t.Fatalf("continued after cancelled probe:\n%s", got)
					}
				})
			}
		}
	}
}

func TestOfflineCancellationDuringInspection(t *testing.T) {
	a, dir, _, log := offlineClusterApp(t, "docker")
	t.Setenv("OFFLINE_BLOCK_IMAGE", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a = a.withRunContext(ctx)
	observed := make(chan bool, 1)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				observed <- false
				return
			case <-tick.C:
				raw, _ := os.ReadFile(log)
				if strings.Contains(string(raw), "engine image inspect") {
					cancel()
					observed <- true
					return
				}
			}
		}
	}()
	err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
	if !<-observed || !errors.Is(err, context.Canceled) {
		t.Fatalf("did not cancel active inspection: %v", err)
	}
	raw, _ := os.ReadFile(log)
	if strings.Contains(string(raw), "kind get clusters") || strings.Contains(string(raw), "config view") {
		t.Fatalf("continued after cancellation: %s", raw)
	}
}

func TestOfflineUpRequiresClusterBeforeAccess(t *testing.T) {
	for _, step := range []string{"", "model", "orka", "ollama", "unknown"} {
		a := &App{}
		if err := a.UpWithOptions(UpOptions{Step: step, OfflineBundle: "does-not-exist"}); err == nil || !strings.Contains(err.Error(), "--step cluster") {
			t.Fatalf("step %q: %v", step, err)
		}
	}
}

func TestOfflineLocalProbesNeverProvision(t *testing.T) {
	a, dir, _, _ := offlineClusterApp(t, "docker")
	t.Setenv("PATH", t.TempDir())
	err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
	if err == nil || !strings.Contains(err.Error(), "3 missing or unusable") {
		t.Fatalf("local missing tools: %v", err)
	}
	if len(a.provisioned) != 0 {
		t.Fatal("local probes provisioned tools")
	}
}

func TestOfflineProductionBuildIdentityRefusesTestBinary(t *testing.T) {
	if _, err := runningOfflineIdentity(); err == nil || !strings.Contains(err.Error(), "unmodified VCS metadata") {
		t.Fatalf("test binary identity should refuse absent VCS metadata: %v", err)
	}
}

func TestOfflineBundleSpecialFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux offline runtime")
	}
	for _, kind := range []string{"directory", "FIFO"} {
		t.Run(kind, func(t *testing.T) {
			dir, m := offlineFixture(t)
			path := filepath.Join(dir, "not-a-file")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if output, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
					t.Fatalf("fixture mkfifo: %v %s", err, output)
				}
			}
			m["node"].(map[string]any)["configPath"] = "not-a-file"
			offlineWriteManifest(t, dir, m)
			if _, err := loadOfflineNodeBundle(dir, offlineTestIdentity()); err == nil || !strings.Contains(err.Error(), "node config: not-a-file:") {
				t.Fatalf("want non-regular node config refusal, got %v", err)
			}
		})
	}
}
