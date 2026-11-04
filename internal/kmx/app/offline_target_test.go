package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func offlineSocketFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return "unix://" + path
}

// Removing locality resolution must let these cases reach version/info/image
// probes. Fakes replace only external CLIs; Up and filesystem checks are real.
func TestOfflineEndpointRefusesBeforeProbes(t *testing.T) {
	for _, mode := range []string{"empty path", "double slash", "opaque", "remote", "loopback", "ssh", "missing", "regular", "authority", "credentials", "query", "fragment", "escaped", "control", "relative", "multiple", "duplicate", "no docker", "tls default", "podman implicit", "podman dual", "podman unknown", "podman duplicate", "podman remote"} {
		t.Run(mode, func(t *testing.T) {
			engine := "docker"
			if strings.HasPrefix(mode, "podman") {
				engine = "podman"
			}
			a, dir, _, log := offlineClusterApp(t, engine)
			uri := os.Getenv("OFFLINE_SOCKET")
			switch mode {
			case "empty path":
				uri = "unix:///"
			case "double slash":
				uri = "unix:////" + strings.TrimPrefix(uri, "unix:///")
			case "opaque":
				uri = "unix:opaque"
			case "remote":
				uri = "tcp://remote.invalid:2375"
			case "loopback":
				uri = "tcp://127.0.0.1:2375"
			case "tls default":
				t.Setenv("DOCKER_TLS_VERIFY", "1")
				uri = "tcp://localhost:2376"
			case "ssh":
				uri = "ssh://private@remote.invalid"
			case "missing":
				uri += "-missing"
			case "regular":
				path := filepath.Join(filepath.Dir(log), "regular")
				os.WriteFile(path, nil, 0600)
				uri = "unix://" + path
			case "authority":
				uri = "unix://localhost" + strings.TrimPrefix(uri, "unix://")
			case "credentials":
				uri = "unix://secret@localhost/s"
			case "query":
				uri += "?secret=1"
			case "fragment":
				uri += "#secret"
			case "escaped":
				uri = strings.Replace(uri, "/s", "/%73", 1)
			case "control":
				uri += "\u0001"
			case "relative":
				uri = "unix:relative"
			case "podman implicit":
				t.Setenv("CONTAINER_HOST", "")
			case "podman dual":
				t.Setenv("CONTAINER_CONNECTION", "chosen")
			case "podman unknown", "podman duplicate", "podman remote":
				t.Setenv("CONTAINER_HOST", "")
				t.Setenv("CONTAINER_CONNECTION", "chosen")
				rows := `[{"Name":"other","URI":"` + uri + `"}]`
				if mode == "podman duplicate" {
					rows = `[{"Name":"chosen","URI":"` + uri + `"},{"Name":"chosen","URI":"` + uri + `"}]`
				}
				if mode == "podman remote" {
					rows = `[{"Name":"chosen","URI":"ssh://private@remote.invalid"}]`
				}
				t.Setenv("OFFLINE_CONNECTIONS", rows)
			}
			rawBytes, err := json.Marshal([]any{map[string]any{"Endpoints": map[string]any{"docker": map[string]string{"Host": uri}}}})
			if err != nil {
				t.Fatal(err)
			}
			raw := string(rawBytes)
			if mode == "multiple" {
				raw = raw[:len(raw)-1] + "," + raw[1:]
			}
			if mode == "duplicate" {
				raw = `[{"Endpoints":{"docker":{"Host":"` + uri + `","Host":"` + uri + `"}}}]`
			}
			if mode == "no docker" {
				raw = `[{"Endpoints":{"other":{"Host":"` + uri + `"}}}]`
			}
			t.Setenv("OFFLINE_DOCKER_CONTEXT_JSON", raw)
			err = a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
			if err == nil {
				t.Fatal("unsafe endpoint accepted")
			}
			calls, _ := os.ReadFile(log)
			for _, forbidden := range []string{"version", " info", "image inspect", "kind get", "config view", "create", " start"} {
				if bytes.Contains(calls, []byte(forbidden)) {
					t.Fatalf("endpoint refusal too late (%v):\n%s", err, calls)
				}
			}
			if strings.Contains(err.Error(), "private@") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("endpoint leaked: %v", err)
			}
		})
	}
}

func TestOfflineEndpointEffectiveSelectionAndFreeze(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, named := range []bool{false, true} {
			t.Run(engine+map[bool]string{false: "/host", true: "/selection"}[named], func(t *testing.T) {
				a, dir, _, log := offlineClusterApp(t, engine)
				uri := os.Getenv("OFFLINE_SOCKET")
				// Stat must follow directory symlinks, but children retain ORIGINAL URI.
				link := filepath.Join(filepath.Dir(strings.TrimPrefix(uri, "unix://")), "alias")
				if err := os.Symlink(strings.TrimSuffix(strings.TrimPrefix(uri, "unix://"), "/s"), link); err != nil {
					t.Fatal(err)
				}
				uri = "unix://" + filepath.Join(link, "s")
				t.Setenv("DOCKER_HOST", "tcp://inherited.invalid:2375")
				t.Setenv("DOCKER_CONTEXT", "inherited")
				t.Setenv("DOCKER_TLS_VERIFY", "1")
				t.Setenv("DOCKER_CERT_PATH", "/private/inherited")
				t.Setenv("KUBERC", "/private/inherited-kuberc")
				t.Setenv("KUBECTL_KUBERC", "true")
				a.Run.Env = append(a.Run.Env, "DOCKER_CONTEXT=copied", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/private/copied", "KUBERC=/private/copied-kuberc", "KUBECTL_KUBERC=true")
				if engine == "docker" {
					t.Setenv("OFFLINE_DOCKER_CONTEXT_JSON", `[{"Endpoints":{"docker":{"Host":"`+uri+`"}}}]`)
					a.Run.Env = append(a.Run.Env, "DOCKER_HOST="+uri)
				} else if named {
					t.Setenv("CONTAINER_HOST", "tcp://inherited.invalid:1")
					a.Run.Unset = append(a.Run.Unset, "CONTAINER_HOST")
					a.Run.Env = append(a.Run.Env, "CONTAINER_CONNECTION=chosen")
					t.Setenv("OFFLINE_CONNECTIONS", `[{"Name":"other","URI":"ssh://elsewhere"},{"Name":"chosen","URI":"`+uri+`"}]`)
				} else {
					t.Setenv("CONTAINER_HOST", "tcp://inherited.invalid:1")
					a.Run.Env = append(a.Run.Env, "CONTAINER_HOST="+uri)
				}
				beforeEnv := strings.Join(a.Run.Env, "\n")
				beforeUnset := strings.Join(a.Run.Unset, "\n")
				t.Setenv("OFFLINE_WANT_HOST", uri)
				// Every later CLI, including engine grandchildren of kind, must
				// inherit frozen selectors. Record violations independently.
				for _, tool := range []string{engine, "kind", "kubectl"} {
					path := filepath.Join(filepath.Dir(log), tool)
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					check := `case "$1 $2" in
"context inspect")
 [ "$*" = "context inspect" ] && [ "$DOCKER_HOST" = "$OFFLINE_WANT_HOST" ] && [ "$DOCKER_CONTEXT" = copied ] || echo metadata >> "$OFFLINE_LOG.bad" ;;
"system connection")
 [ "$*" = "system connection list --format=json" ] && [ -z "$CONTAINER_HOST" ] && [ "$CONTAINER_CONNECTION" = chosen ] || echo metadata >> "$OFFLINE_LOG.bad" ;;
*)
if [ "` + engine + `" = docker ]; then
 [ "$DOCKER_HOST" = "$OFFLINE_WANT_HOST" ] && [ -z "$DOCKER_CONTEXT$DOCKER_TLS_VERIFY$DOCKER_CERT_PATH" ] || echo unfrozen >> "$OFFLINE_LOG.bad"
else
 [ "$CONTAINER_HOST" = "$OFFLINE_WANT_HOST" ] && [ -z "$CONTAINER_CONNECTION" ] || echo unfrozen >> "$OFFLINE_LOG.bad"
fi
[ "$KIND_EXPERIMENTAL_PROVIDER" = "` + engine + `" ] || echo provider >> "$OFFLINE_LOG.bad"
[ "$KUBERC" = off ] && [ "$KUBECTL_KUBERC" = false ] || echo preferences >> "$OFFLINE_LOG.bad"
;; esac
`
					if err := os.WriteFile(path, []byte(strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\n"+check, 1)), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir}); err != nil {
					t.Fatal(err)
				}
				if bad, _ := os.ReadFile(log + ".bad"); len(bad) != 0 {
					t.Fatalf("unfrozen children: %s", bad)
				}
				if strings.Join(a.Run.Env, "\n") != beforeEnv || strings.Join(a.Run.Unset, "\n") != beforeUnset {
					t.Fatal("shared Runner changed")
				}
			})
		}
	}
}

// Configuration command failures must remain bounded/private and preserve
// cancellation, just like the already-bounded image and version probes.
func TestOfflineTargetMetadataSafety(t *testing.T) {
	for _, stage := range []string{"docker context inspect", "podman system connection list --format=json", "kubectl config view -o json", "kubectl --context kind-test config view --raw --minify -o jsonpath={.clusters[0].cluster}", "kind get kubeconfig --name test", "kind get clusters"} {
		for _, mode := range []string{"stderr", "oversize", "cancel", "deadline"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				engine := "docker"
				if strings.HasPrefix(stage, "podman") {
					engine = "podman"
				}
				a, dir, _, log := offlineClusterApp(t, engine)
				t.Setenv("OFFLINE_EXISTING", "test")
				t.Setenv("OFFLINE_CONTEXT_MISSING", "")
				if engine == "podman" {
					t.Setenv("CONTAINER_HOST", "")
					t.Setenv("CONTAINER_CONNECTION", "selected")
				}
				tool, args, _ := strings.Cut(stage, " ")
				path := filepath.Join(filepath.Dir(log), tool)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				action := `echo private-target-canary >&2; exit 1`
				if mode == "oversize" {
					payload := filepath.Join(filepath.Dir(log), "payload")
					if err := os.WriteFile(payload, bytes.Repeat([]byte("x"), offlineJSONLimit+1), 0600); err != nil {
						t.Fatal(err)
					}
					action = "/bin/cat " + shellArg(payload) + "; exit 0"
				}
				if mode == "cancel" || mode == "deadline" {
					action = "exec /bin/sleep 30"
				}
				prefix := `if [ "$*" = ` + shellArg(args) + ` ]; then
echo reached > "$OFFLINE_LOG.stage"
` + action + `
fi
`
				if err := os.WriteFile(path, []byte(strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\n"+prefix, 1)), 0700); err != nil {
					t.Fatal(err)
				}
				var want error
				if mode == "cancel" || mode == "deadline" {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					a = a.withRunContext(ctx)
					want = context.DeadlineExceeded
					if mode == "cancel" {
						want = context.Canceled
						go func() {
							tick := time.NewTicker(time.Millisecond)
							defer tick.Stop()
							for {
								select {
								case <-ctx.Done():
									return
								case <-tick.C:
									if _, err := os.Stat(log + ".stage"); err == nil {
										cancel()
										return
									}
								}
							}
						}()
					}
				}
				err = a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				if err == nil || want != nil && !errors.Is(err, want) {
					t.Fatalf("metadata failure lost (%v): %v", want, err)
				}
				if _, err := os.Stat(log + ".stage"); err != nil {
					t.Fatal("did not exercise selected metadata stage")
				}
				output := a.Out.(*bytes.Buffer).String() + err.Error()
				if len(output) > 4096 || strings.Contains(output, "private-target-canary") || strings.Contains(output, strings.Repeat("x", 100)) {
					t.Fatal("metadata body leaked")
				}
				calls, _ := os.ReadFile(log)
				for _, forbidden := range []string{"kind export", "kind create", "engine start", "get --raw=/readyz", "rollout status"} {
					if bytes.Contains(calls, []byte(forbidden)) {
						t.Fatalf("continued after failed metadata: %s", calls)
					}
				}
			})
		}
	}
}
