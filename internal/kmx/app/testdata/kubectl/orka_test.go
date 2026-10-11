package kubectl

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fake lives at the executable boundary: generation, schemas, ordering,
// polling, token decoding and HTTP are all the real app code.
func TestOrkaKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_ORKA_TEST_DIR")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	// Dependency probing is local, not a context-bound cluster operation.
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	scenario := os.Getenv("KMX_ORKA_TEST_SCENARIO")
	log, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	call := orkaCall{Args: args}
	if slices.Contains(args, "-f") {
		body, _ := io.ReadAll(os.Stdin)
		_ = json.Unmarshal(body, &call.Document)
	}
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	fail := func() { fmt.Fprint(os.Stderr, "external failure "+orkaTestToken()); os.Exit(1) }
	for _, entry := range os.Environ() {
		if strings.Contains(entry, orkaTestToken()) {
			fail()
		}
	}
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	if slices.Contains(args, "config") {
		server := "https://127.0.0.1:6443"
		if scenario == "guard" {
			server = "https://managed.example.invalid"
		}
		fmt.Printf(`{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":%q}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`, server)
		os.Exit(0)
	}
	if slices.Contains(args, "port-forward") {
		_ = os.WriteFile(filepath.Join(dir, "forward-pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
		if scenario == "forward-hang" {
			time.Sleep(time.Hour)
		}
		if scenario == "forward-fail" {
			fail()
		}
		port, _, _ := strings.Cut(args[len(args)-1], ":")
		if scenario == "owned-forward" {
			listener, err := net.Listen("tcp", "127.0.0.1:"+port)
			if err != nil {
				fail()
			}
			fmt.Println("Forwarding from 127.0.0.1:" + port + " -> 8080")
			_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			}))
			os.Exit(0)
		}
		fmt.Println("Forwarding from 127.0.0.1:" + port + " -> 8080")
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 {
		if scenario == "oversized-get" {
			fmt.Print(strings.Repeat("x", 5<<20))
			os.Exit(0)
		}
		if scenario == "hang-get" {
			time.Sleep(time.Hour)
		}
		kind, name := args[i+1], args[i+2]
		if scenario == "denied-provider-read" && kind == "providers.core.orka.ai" {
			fail()
		}
		switch kind {
		case "crd":
			if scenario == "missing-crd" {
				os.Exit(0)
			}
			if scenario == "denied-crd" {
				fail()
			}
			target := "v0.1.3"
			if scenario == "main" {
				target = "main"
			}
			plural, _, _ := strings.Cut(name, ".")
			body, err := os.ReadFile(filepath.Join(os.Getenv("KMX_ORKA_TEST_FIXTURES"), target, plural+".yaml"))
			if err != nil {
				fail()
			}
			_, _ = os.Stdout.Write(body)
		case "secret":
			switch scenario {
			case "missing-secret":
			case "missing-key":
				fmt.Print("secret\n")
			case "denied-secret":
				fail()
			case "invalid-marker":
				fmt.Print("garbage")
			default:
				fmt.Print("secret\npresent")
			}
		case "serviceaccount":
			fmt.Print("serviceaccount/" + name)
		case "service":
			fmt.Print(`{"spec":{"ports":[{"port":8080}]}}`)
		default:
			if !strings.HasSuffix(kind, ".core.orka.ai") {
				fail()
			}
			if slices.Contains(args, "name") {
				if scenario == "task-collision" && kind == "tasks.core.orka.ai" {
					fmt.Print(kind + "/" + name)
				}
				if scenario == "denied-collision" {
					fail()
				}
			} else {
				if scenario == "denied-collision" && slices.Contains(args, "--ignore-not-found=true") {
					fail()
				}
				raw, err := os.ReadFile(filepath.Join(dir, name+"-"+kind+".json"))
				if err != nil {
					if os.IsNotExist(err) && slices.Contains(args, "--ignore-not-found=true") {
						os.Exit(0)
					}
					fail()
				}
				var obj map[string]any
				_ = json.Unmarshal(raw, &obj)
				meta := obj["metadata"].(map[string]any)
				if scenario == "terminating-"+strings.ToLower(obj["kind"].(string)) {
					meta["deletionTimestamp"] = "2026-01-01T00:00:00Z"
					meta["finalizers"] = []string{"orka.ai/cleanup"}
				}
				if scenario == "replacement" {
					meta["uid"] = "replacement"
				}
				if scenario == "changed-generation" {
					meta["generation"] = 2
				}
				status := map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
				if _, err := os.Stat(filepath.Join(dir, "provider-not-ready")); err == nil && obj["kind"] == "Provider" {
					status = map[string]any{"ready": false, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "observedGeneration": meta["generation"]}}}
				}
				if scenario == "stale-ready" || scenario == "stale-agent" && obj["kind"] == "Agent" {
					status["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 0}}
				}
				if obj["kind"] == "Task" {
					// Orka v0.1.3 adds a finalizer via whole-object Update. Its
					// nonpointer ResourceRequirements serializes as {}, changing
					// the spec (and generation) only when resources was absent.
					if scenario == "task-resources-roundtrip" {
						spec := obj["spec"].(map[string]any)
						if _, exists := spec["resources"]; !exists {
							spec["resources"] = map[string]any{}
							meta["generation"] = meta["generation"].(float64) + 1
						}
						meta["finalizers"] = []string{"orka.ai/cleanup"}
						body, err := json.Marshal(obj)
						if err != nil {
							fail()
						}
						if err := os.WriteFile(filepath.Join(dir, name+"-"+kind+".json"), body, 0600); err != nil {
							fail()
						}
					}
					status = map[string]any{"phase": "Succeeded", "resultRef": map[string]any{"available": true}}
					if scenario == "failed-task" {
						status["phase"] = "Failed"
					}
					if scenario == "unavailable-task" {
						status["resultRef"] = map[string]any{"available": false}
					}
				}
				obj["status"] = status
				_ = json.NewEncoder(os.Stdout).Encode(obj)
			}
		}
		os.Exit(0)
	}
	if pi := slices.Index(args, "patch"); pi >= 0 {
		// Only the marker refresh patches: a resourceVersion test, then
		// replace ops on annotations.
		var ops []map[string]any
		path := filepath.Join(dir, args[pi+2]+"-"+args[pi+1]+".json")
		raw, err := os.ReadFile(path)
		if err != nil || !slices.Contains(args, "--type=json") || json.Unmarshal([]byte(args[slices.Index(args, "-p")+1]), &ops) != nil || len(ops) < 2 {
			fail()
		}
		var live map[string]any
		_ = json.Unmarshal(raw, &live)
		meta := live["metadata"].(map[string]any)
		if ops[0]["op"] != "test" || ops[0]["path"] != "/metadata/resourceVersion" || ops[0]["value"] != meta["resourceVersion"] {
			fail()
		}
		annotations := meta["annotations"].(map[string]any)
		for _, op := range ops[1:] {
			key, ok := strings.CutPrefix(fmt.Sprint(op["path"]), "/metadata/annotations/")
			if op["op"] != "replace" || !ok {
				fail()
			}
			annotations[strings.NewReplacer("~1", "/", "~0", "~").Replace(key)] = op["value"]
		}
		meta["resourceVersion"] = fmt.Sprint(meta["resourceVersion"]) + "+"
		body, _ := json.Marshal(live)
		_ = os.WriteFile(path, body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	if slices.Contains(args, "replace") {
		if slices.Contains(args, "--dry-run=server") {
			_ = json.NewEncoder(os.Stdout).Encode(call.Document)
			os.Exit(0)
		}
		meta := call.Document["metadata"].(map[string]any)
		path := filepath.Join(dir, meta["name"].(string)+"-"+strings.ToLower(call.Document["kind"].(string))+"s.core.orka.ai.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			fail()
		}
		var live map[string]any
		_ = json.Unmarshal(raw, &live)
		current := live["metadata"].(map[string]any)
		if meta["resourceVersion"] != current["resourceVersion"] {
			fail()
		}
		meta["resourceVersion"] = fmt.Sprint(current["resourceVersion"]) + "+"
		if fmt.Sprint(live["spec"]) != fmt.Sprint(call.Document["spec"]) {
			meta["generation"] = current["generation"].(float64) + 1
		}
		body, _ := json.Marshal(call.Document)
		_ = os.WriteFile(path, body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	if slices.Contains(args, "create") {
		if slices.Contains(args, "token") {
			// The account name is whatever the caller selected (`reader` in the
			// create tests, `orka-result-reader` on the quickstart path). Match
			// its suffix so the fake still refuses a token for anything else.
			if !slices.Contains(args, "--duration=10m") || !slices.Contains(args, "json") ||
				!slices.ContainsFunc(args, func(s string) bool { return strings.HasSuffix(s, "reader") }) {
				fail()
			}
			if scenario == "token-fail" {
				fail()
			}
			expiry := time.Now().Add(10 * time.Minute)
			if scenario == "short-token" {
				expiry = time.Now().Add(time.Minute)
			}
			fmt.Printf(`{"status":{"token":%q,"expirationTimestamp":%q}}`, orkaTestToken(), expiry.Format(time.RFC3339))
			os.Exit(0)
		}
		if call.Document == nil || call.Document["kind"] == "Secret" {
			fail()
		}
		if slices.Contains(args, "--dry-run=server") {
			if scenario == "admission" {
				fail()
			}
			fmt.Print("{}")
			os.Exit(0)
		}
		if scenario == "create-race" || scenario == "ambiguous-task" && call.Document["kind"] == "Task" {
			fail()
		}
		meta := call.Document["metadata"].(map[string]any)
		meta["uid"] = strings.ToLower(call.Document["kind"].(string)) + "-uid"
		meta["generation"] = 1
		meta["resourceVersion"] = "1"
		body, _ := json.Marshal(call.Document)
		_ = os.WriteFile(filepath.Join(dir, meta["name"].(string)+"-"+strings.ToLower(call.Document["kind"].(string))+"s.core.orka.ai.json"), body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	fail()
}
