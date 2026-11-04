package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func offlineSelectedConfigTools(t *testing.T, log string, merged string, selected string, expected string) {
	t.Helper()
	t.Setenv("OFFLINE_MERGED", merged)
	t.Setenv("OFFLINE_SELECTED_CLUSTER", selected)
	t.Setenv("OFFLINE_EXPECTED_CONFIG", expected)
	fakeTool(t, filepath.Dir(log), "kubectl", `printf 'kubectl %s|kube=%s\n' "$*" "$KUBECONFIG" >> "$OFFLINE_LOG"
case "$*" in
"version --client") ;;
"config view -o json") printf '%s' "$OFFLINE_MERGED" ;;
"--context kind-test config view --raw --minify -o jsonpath={.clusters[0].cluster}") printf '%s' "$OFFLINE_SELECTED_CLUSTER" ;;
"--context kind-test get --raw=/readyz"|"--context kind-test -n kube-system rollout status deployment/coredns --timeout=180s") ;;
*) echo private-config-stderr >&2; exit 98;; esac`)
}

// Without the identity check, these entries reach readiness/start or creation.
// Different credentials remain deliberately irrelevant to cluster identity.
func TestOfflineKubeconfigBinding(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, mode := range []string{"same", "different credentials", "server", "CA", "TLS", "insecure", "proxy", "broken ref", "empty ref", "empty server", "duplicate context", "duplicate cluster", "missing", "absent cluster", "absent broken", "absent duplicate", "expected missing", "expected malformed", "expected broken", "expected duplicate", "expected unavailable", "stopped", "projection error", "projection malformed", "oversize", "file equivalent", "symlink CA", "data wins", "wrong data wins", "relative CA", "missing CA", "directory CA", "large CA"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				a, dir, _, log := offlineClusterApp(t, engine)
				t.Setenv("OFFLINE_EXISTING", "test\n")
				t.Setenv("OFFLINE_CONTEXT_MISSING", "")
				merged := `{"contexts":[{"name":"kind-test","context":{"cluster":"chosen","user":"different-user"}}],"clusters":[{"name":"chosen","cluster":{"server":"https://127.0.0.1:6443"}}],"users":[{"name":"different-user","user":{"exec":{"command":"NEVER-EXECUTE-AUTH"}}}]}`
				cluster := map[string]any{"server": "https://127.0.0.1:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString([]byte("same CA"))}
				expected := strings.Replace(os.Getenv("OFFLINE_EXPECTED_CONFIG"), `cluster: {server: "https://127.0.0.1:6443"}`, `cluster: {server: "https://127.0.0.1:6443", certificate-authority-data: c2FtZSBDQQ==}`, 1)
				switch mode {
				case "server":
					cluster["server"] = "https://127.0.0.1:6444/other"
				case "CA":
					cluster["certificate-authority-data"] = base64.StdEncoding.EncodeToString([]byte("foreign CA"))
				case "TLS":
					cluster["tls-server-name"] = "foreign.invalid"
				case "insecure":
					cluster["insecure-skip-tls-verify"] = true
				case "proxy":
					cluster["proxy-url"] = "http://127.0.0.1:3128"
				case "broken ref", "absent broken":
					merged = strings.Replace(merged, `"cluster":"chosen"`, `"cluster":"missing"`, 1)
				case "empty ref":
					merged = strings.Replace(merged, `"cluster":"chosen"`, `"cluster":""`, 1)
				case "empty server":
					merged = strings.Replace(merged, "https://127.0.0.1:6443", "", 1)
				case "duplicate context", "absent duplicate":
					merged = strings.Replace(merged, `"contexts":[`, `"contexts":[{"name":"kind-test","context":{"cluster":"chosen"}},`, 1)
				case "duplicate cluster":
					merged = strings.Replace(merged, `"clusters":[`, `"clusters":[{"name":"chosen","cluster":{"server":"https://127.0.0.1:6443"}},`, 1)
				case "missing":
					merged = `{"contexts":[],"clusters":[]}`
				case "expected missing":
					expected = "apiVersion: v1\nkind: Config\n"
				case "expected malformed":
					expected = "cluster: [private-config-canary"
				case "expected broken":
					expected = strings.Replace(expected, "context: {cluster: kind-test", "context: {cluster: missing", 1)
				case "expected duplicate":
					expected = strings.Replace(expected, "contexts:\n", "contexts:\n- name: kind-test\n  context: {cluster: kind-test}\n", 1)
				case "projection malformed":
					cluster = nil
				case "oversize":
					expected += strings.Repeat("#", offlineJSONLimit+1)
				case "file equivalent", "symlink CA", "data wins", "wrong data wins", "relative CA", "missing CA", "directory CA", "large CA":
					path := filepath.Join(filepath.Dir(log), "ca")
					data := []byte("same CA")
					if mode == "data wins" {
						data = []byte("wrong file CA")
					}
					if mode == "large CA" {
						data = bytes.Repeat([]byte("x"), offlineJSONLimit+1)
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
					if mode == "symlink CA" {
						link := path + "-link"
						if err := os.Symlink(path, link); err != nil {
							t.Fatal(err)
						}
						path = link
					}
					if mode == "directory CA" {
						path = filepath.Dir(log)
					}
					if mode == "relative CA" {
						path = "unresolved-relative-ca"
					}
					if mode == "missing CA" {
						path += "-missing"
					}
					cluster["certificate-authority"] = path
					if mode == "wrong data wins" {
						cluster["certificate-authority-data"] = "d3Jvbmc="
					} else if mode != "data wins" {
						delete(cluster, "certificate-authority-data")
					}
				}
				if strings.HasPrefix(mode, "absent") {
					t.Setenv("OFFLINE_EXISTING", "")
				}
				raw, err := json.Marshal(cluster)
				if err != nil {
					t.Fatal(err)
				}
				offlineSelectedConfigTools(t, log, merged, string(raw), expected)
				if mode == "projection error" {
					path := filepath.Join(filepath.Dir(log), "kubectl")
					body, _ := os.ReadFile(path)
					body = bytes.Replace(body, []byte(`printf '%s' "$OFFLINE_SELECTED_CLUSTER"`), []byte(`echo private-config-canary >&2; exit 1`), 1)
					os.WriteFile(path, body, 0700)
				}
				if mode == "expected unavailable" || mode == "stopped" {
					path := filepath.Join(filepath.Dir(log), "kind")
					body, _ := os.ReadFile(path)
					body = bytes.Replace(body, []byte(`printf '%s' "$OFFLINE_EXPECTED_CONFIG"`), []byte(`echo private-config-canary >&2; exit 1`), 1)
					os.WriteFile(path, body, 0700)
				}
				err = a.UpWithOptions(UpOptions{Step: "cluster", OfflineBundle: dir})
				valid := mode == "same" || mode == "different credentials" || mode == "file equivalent" || mode == "symlink CA" || mode == "data wins" || mode == "missing"
				if (err == nil) != valid {
					t.Fatalf("valid=%v: %v", valid, err)
				}
				if mode == "relative CA" && !strings.Contains(err.Error(), "unresolved CA file origin") {
					t.Fatalf("relative CA origin was guessed or failed elsewhere: %v", err)
				}
				calls, _ := os.ReadFile(log)
				if !valid {
					for _, forbidden := range []string{"kind export", "kind create", "engine start", "get --raw=/readyz", "rollout status"} {
						if bytes.Contains(calls, []byte(forbidden)) {
							t.Fatalf("identity refusal too late (%v):\n%s", err, calls)
						}
					}
					if strings.Contains(err.Error(), "private-config") || strings.Contains(err.Error(), "foreign.invalid") {
						t.Fatalf("private config leaked: %v", err)
					}
				} else if !bytes.Contains(calls, []byte("get --raw=/readyz")) {
					t.Fatalf("valid reuse did not finish: %s", calls)
				}
				if mode == "missing" && !bytes.Contains(calls, []byte("kind export kubeconfig")) {
					t.Fatal("missing context not exported")
				}
				if mode != "missing" && valid && bytes.Contains(calls, []byte("kind export")) {
					t.Fatal("existing context overwritten")
				}
				if valid && mode != "missing" {
					nodes := bytes.Index(calls, []byte("engine container inspect"))
					projection := bytes.Index(calls, []byte("config view --raw --minify -o jsonpath={.clusters[0].cluster}"))
					proof := bytes.Index(calls, []byte("kind get kubeconfig --name test"))
					if nodes < 0 || projection < nodes || proof < projection {
						t.Fatalf("proof not after verified node: %s", calls)
					}
				}
				if bytes.Contains(calls, []byte("--flatten")) || bytes.Contains(calls, []byte("NEVER-EXECUTE-AUTH")) {
					t.Fatal("unsafe config read")
				}
			})
		}
	}
}

func TestOfflineCACancellation(t *testing.T) {
	a, _, _, _ := offlineClusterApp(t, "docker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a = a.withRunContext(ctx)
	for _, c := range []offlineClusterConnection{{CAFile: "/unavailable"}, {CAData: "Y2E="}, {}} {
		if _, err := a.offlineCA(c); !errors.Is(err, context.Canceled) {
			t.Fatalf("CA read lost cancellation: %v", err)
		}
	}
}
