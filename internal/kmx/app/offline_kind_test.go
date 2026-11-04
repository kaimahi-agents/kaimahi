package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
