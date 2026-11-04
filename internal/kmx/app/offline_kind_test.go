package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOfflineKindRelativeTemp(t *testing.T) {
	a, _, _, _ := offlineClusterApp(t, "docker")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", relative)
	runner, cleanup, err := a.offlineKindRunner()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, entry := range runner.Command("/bin/sh").Environ() {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			first, _, _ := strings.Cut(value, string(os.PathListSeparator))
			if !filepath.IsAbs(first) {
				t.Fatalf("relative guard PATH entry: %q", first)
			}
		}
	}
}

func TestOfflineKindSelectedProvider(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			a, dir, _, log := offlineClusterApp(t, engine)
			t.Setenv("OFFLINE_ENGINE", engine)
			before := append([]string(nil), a.Run.Env...)
			fakeTool(t, filepath.Dir(log), "nerdctl", `echo 'alternate engine pull' >> "$OFFLINE_LOG"`)
			fakeTool(t, filepath.Dir(log), engine, `printf 'engine %s\n' "$*" >> "$OFFLINE_LOG"
case "$1 $2" in
"context inspect") printf '%s' "$OFFLINE_DOCKER_CONTEXT_JSON" ;;
"info ") ;;
"image inspect") /bin/cat "$OFFLINE_IMAGE_JSON" ;;
"info --format") exit 1 ;;
*) exit 98;; esac`)
			fakeTool(t, filepath.Dir(log), "kind", `case "$1 $2" in
"version "|"get clusters") ;;
"create cluster")
  if ! "$OFFLINE_ENGINE" info --format '{{json .}}'; then
    if [ "$KIND_EXPERIMENTAL_PROVIDER" != "$OFFLINE_ENGINE" ]; then
      nerdctl pull unreachable.invalid/node
    fi
    exit 1
  fi ;;
*) exit 98;; esac`)
			err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
			calls, _ := os.ReadFile(log)
			if err == nil || !bytes.Contains(calls, []byte("engine info --format")) || bytes.Contains(calls, []byte("alternate engine pull")) {
				t.Fatalf("kind changed selected provider after transient info failure: %v\n%s", err, calls)
			}
			if !reflect.DeepEqual(before, a.Run.Env) {
				t.Fatal("offline provider selection changed shared Runner")
			}
		})
	}
}

// Only the dedicated executed sentinel is proof; nil/other exits, mode-based
// execution refusal and cancellation cannot authorize an unguarded child.
func TestOfflineKindGuardExecutionProbe(t *testing.T) {
	for _, mode := range []string{"sentinel", "success", "wrong exit", "nonexecutable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _, log := offlineClusterApp(t, "docker")
			path := filepath.Join(t.TempDir(), "guard")
			body := "#!/bin/sh\n[ \"$1\" = __kmx_offline_guard_probe__ ] || exit 99\nexit 42\n"
			if mode == "success" {
				body = "#!/bin/sh\nexit 0\n"
			} else if mode == "wrong exit" {
				body = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(path, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "nonexecutable" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancelled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				a = a.withRunContext(ctx)
			}
			err := a.verifyOfflineKindGuard(path)
			if mode == "sentinel" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "offline kind guard execution") {
				t.Fatalf("invalid execution proof accepted: %v", err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if raw, _ := os.ReadFile(log); len(raw) != 0 {
				t.Fatalf("probe reached kind/engine: %s", raw)
			}
		})
	}
}

// Discover an execution-blocked temporary filesystem when one is available;
// shipped tests must not require a particular host mount or privileged setup.
func offlineExecutionBlockedTemp(t *testing.T) string {
	t.Helper()
	for _, root := range []string{"/run/lock", "/dev/shm"} {
		dir, err := os.MkdirTemp(root, "kmx-guard-test-")
		if err != nil {
			continue
		}
		path := filepath.Join(dir, "probe")
		err = os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700)
		if err == nil {
			err = exec.Command(path).Run()
		}
		if errors.Is(err, os.ErrPermission) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
		_ = os.RemoveAll(dir)
	}
	t.Skip("no writable execution-blocked temporary filesystem available")
	return ""
}

func TestOfflineKindExecutionBlockedTemp(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		t.Run(engine, func(t *testing.T) {
			a, dir, _, log := offlineClusterApp(t, engine)
			blocked := offlineExecutionBlockedTemp(t)
			t.Logf("execution-blocked temporary directory: %s", blocked)
			t.Setenv("TMPDIR", blocked)
			t.Setenv("OFFLINE_ENGINE", engine)
			fakeTool(t, filepath.Dir(log), "kind", `printf 'kind %s\n' "$*" >> "$OFFLINE_LOG"
case "$1 $2" in
"version "|"get clusters") ;;
"create cluster") "$OFFLINE_ENGINE" pull unreachable.invalid/node; exit 1 ;;
*) exit 98;; esac`)
			err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
			calls, _ := os.ReadFile(log)
			if err == nil || !strings.Contains(err.Error(), "offline kind guard execution") || bytes.Contains(calls, []byte("kind create cluster")) || bytes.Contains(calls, []byte("engine pull")) {
				t.Fatalf("unguarded kind creation after execution failure: %v\n%s", err, calls)
			}
			entries, readErr := os.ReadDir(blocked)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("guard retained after failed execution probe: %v %v", entries, readErr)
			}
		})
	}
}

// Exercise the child PATH boundary through real processes, not a kind binary
// from the developer's machine. Removing the shim must expose an engine pull;
// omitting --pull=never must expose a run/create with the default pull policy.
func TestOfflineKindEngineBoundary(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, action := range []string{"pull", "image pull", "transient inspect", "run", "create", "inspect"} {
			t.Run(engine+"/"+action, func(t *testing.T) {
				a, dir, b, log := offlineClusterApp(t, engine)
				// Quotes/spaces in the original executable path must be literal shell
				// data, and the shim must delegate to this absolute path, not itself.
				tools := filepath.Join(t.TempDir(), "engine ' quoted")
				if err := os.Mkdir(tools, 0700); err != nil {
					t.Fatal(err)
				}
				fakeTool(t, tools, engine, `printf 'engine %s|kube=%s\n' "$*" "$KUBECONFIG" >> "$OFFLINE_LOG"
case "$1 $2" in
"context inspect") printf '%s' "$OFFLINE_DOCKER_CONTEXT_JSON" ;;
"info ") ;;
"image inspect") /bin/cat "$OFFLINE_IMAGE_JSON" ;;
"inspect --type=image") echo transient-inspection-failure >&2; exit 1 ;;
*) ;;
esac`)
				t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("OFFLINE_ENGINE", engine)
				t.Setenv("OFFLINE_ACTION", action)
				t.Setenv("OFFLINE_REF", b.node.LocalRef)
				fakeTool(t, filepath.Dir(log), "kind", `case "$1 $2" in
"version "|"get clusters") ;;
"create cluster")
  printf '%s' "$PATH" > "$OFFLINE_LOG.path"
  printf '%s' "$OFFLINE_RUNNER_MARKER" > "$OFFLINE_LOG.marker"
  /bin/cat > /dev/null
  case "$OFFLINE_ACTION" in
  "pull") "$OFFLINE_ENGINE" pull "$OFFLINE_REF" ;;
  "image pull") "$OFFLINE_ENGINE" image pull "$OFFLINE_REF" ;;
  "transient inspect")
    if ! "$OFFLINE_ENGINE" inspect --type=image "$OFFLINE_REF"; then
      "$OFFLINE_ENGINE" pull "$OFFLINE_REF"
    fi
    exit 1 ;;
  "run"|"create") "$OFFLINE_ENGINE" "$OFFLINE_ACTION" --name "literal ' node" "$OFFLINE_REF" ;;
  "inspect") "$OFFLINE_ENGINE" image inspect "$OFFLINE_REF" >/dev/null ;;
  esac ;;
*) exit 98;; esac`)
				a.Run.Env = append(a.Run.Env, "OFFLINE_RUNNER_MARKER=preserved")
				beforeEnv := append([]string(nil), a.Run.Env...)
				beforeUnset := append([]string(nil), a.Run.Unset...)
				beforePath := os.Getenv("PATH")
				err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				raw, _ := os.ReadFile(log)
				calls := string(raw)
				if strings.Contains(calls, "engine pull ") || strings.Contains(calls, "engine image pull ") {
					t.Fatalf("kind reached underlying registry pull: %v\n%s", err, calls)
				}
				blocked := action == "pull" || action == "image pull" || action == "transient inspect"
				if blocked {
					if err == nil || !strings.Contains(err.Error(), "effects are retained") {
						t.Fatalf("pull fallback did not refuse creation: %v", err)
					}
				} else if err != nil {
					t.Fatalf("local engine command refused: %v\n%s", err, calls)
				}
				if action == "run" || action == "create" {
					if !strings.Contains(calls, "engine "+action+" --pull=never --name literal ' node "+b.node.LocalRef) {
						t.Fatalf("implicit download not disabled or literal arguments changed:\n%s", calls)
					}
				}
				if action == "transient inspect" && !strings.Contains(calls, "engine inspect --type=image "+b.node.LocalRef) {
					t.Fatalf("did not reach kind's independent failing inspection:\n%s", calls)
				}
				if beforePath != os.Getenv("PATH") || !reflect.DeepEqual(beforeEnv, a.Run.Env) || !reflect.DeepEqual(beforeUnset, a.Run.Unset) {
					t.Fatal("offline creation mutated shared PATH/Runner environment")
				}
				marker, _ := os.ReadFile(log + ".marker")
				if string(marker) != "preserved" {
					t.Fatalf("lost Runner environment: %q", marker)
				}
				childPath, _ := os.ReadFile(log + ".path")
				shimDir, _, _ := strings.Cut(string(childPath), string(os.PathListSeparator))
				if string(childPath) == beforePath || !bytes.HasSuffix(childPath, []byte(beforePath)) {
					t.Fatalf("kind did not receive an isolated prepended PATH: %q", childPath)
				}
				if _, statErr := os.Stat(shimDir); !os.IsNotExist(statErr) {
					t.Fatalf("private shim directory retained after kind exit: %v", statErr)
				}
			})
		}
	}
}
