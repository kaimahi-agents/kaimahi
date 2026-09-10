package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type orkaCall struct {
	Args     []string
	Document map[string]any
}

// The fake lives at the executable boundary: generation, schemas, ordering,
// polling, token decoding and HTTP are all the real app code.
func TestOrkaKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_ORKA_TEST_DIR")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
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
				if scenario == "collision" || scenario == "identical-provider" || scenario == "task-collision" && kind == "tasks.core.orka.ai" {
					fmt.Print(kind + "/" + name)
				}
				if scenario == "denied-collision" {
					fail()
				}
			} else {
				raw, err := os.ReadFile(filepath.Join(dir, name+"-"+kind+".json"))
				if err != nil {
					fail()
				}
				var obj map[string]any
				_ = json.Unmarshal(raw, &obj)
				meta := obj["metadata"].(map[string]any)
				if scenario == "replacement" {
					meta["uid"] = "replacement"
				}
				if scenario == "changed-generation" {
					meta["generation"] = 2
				}
				status := map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1}}}
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
	if slices.Contains(args, "create") {
		if slices.Contains(args, "token") {
			if !slices.Contains(args, "--duration=10m") || !slices.Contains(args, "json") || !slices.Contains(args, "reader") {
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
		body, _ := json.Marshal(call.Document)
		_ = os.WriteFile(filepath.Join(dir, meta["name"].(string)+"-"+strings.ToLower(call.Document["kind"].(string))+"s.core.orka.ai.json"), body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	fail()
}

func orkaTestToken() string { return "private-" + "session-token-never-print" }

func orkaCreateFixture(t *testing.T, scenario string) (*App, CreateOptions, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", "exec "+shellArg(exe)+" -test.run=^TestOrkaKubectlHelper$ -- \"$@\"")
	fixtures, err := filepath.Abs("../orkaschema/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_ORKA_TEST_DIR", dir)
	t.Setenv("KMX_ORKA_TEST_FIXTURES", fixtures)
	t.Setenv("KMX_ORKA_TEST_SCENARIO", scenario)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Out: out, Err: diagnostics, Run: &run.Runner{Stdout: out, Stderr: diagnostics, Echo: true}}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(dir, "bundle.yaml")}
	return a, opt, out, diagnostics, dir
}

func orkaCalls(t *testing.T, dir string) []orkaCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []orkaCall
	dec := json.NewDecoder(f)
	for {
		var c orkaCall
		err := dec.Decode(&c)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

func TestOrkaOnlineCreatesSeparateStrictObjectsAndWaitsInOrder(t *testing.T) {
	a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range orkaCalls(t, dir) {
		if len(c.Args) < 2 || c.Args[0] != "--context" || c.Args[1] != "kind-test" {
			t.Fatalf("unpinned: %v", c.Args)
		}
		if c.Document != nil {
			kind := c.Document["kind"].(string)
			if kind == "Secret" || !slices.Contains(c.Args, "create") || !slices.Contains(c.Args, "--validate=strict") {
				t.Fatalf("unsafe write: %+v", c)
			}
			if slices.Contains(c.Args, "--dry-run=server") {
				order = append(order, "validate-"+kind)
			} else {
				order = append(order, "create-"+kind)
			}
		} else if slices.Contains(c.Args, "get") && !slices.Contains(c.Args, "crd") && slices.Contains(c.Args, "json") && slices.Contains(c.Args, "providers.core.orka.ai") {
			order = append(order, "ready-Provider")
		} else if slices.Contains(c.Args, "get") && !slices.Contains(c.Args, "crd") && slices.Contains(c.Args, "json") && slices.Contains(c.Args, "agents.core.orka.ai") {
			order = append(order, "ready-Agent")
		}
	}
	want := []string{"validate-Provider", "validate-Agent", "create-Provider", "ready-Provider", "create-Agent", "ready-Agent"}
	if !slices.Equal(order, want) {
		t.Fatalf("order=%v", order)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "no model response was tested") {
		t.Fatalf("misleading output %s %s", out, diagnostics)
	}
}

func TestOrkaPreflightFailureNeverEmitsOrMutates(t *testing.T) {
	for _, scenario := range []string{"guard", "missing-crd", "denied-crd", "collision", "identical-provider", "denied-collision", "missing-secret", "missing-key", "denied-secret", "invalid-marker", "admission", "main", "task-collision"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, scenario)
			if scenario == "main" {
				opt.AgentRequestsPerMinute = "1"
			}
			if scenario == "task-collision" {
				opt.Task = "Say hello"
				opt.ResultServiceAccount = "reader"
			}
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if out.Len() != 0 {
				t.Fatal("preflight emitted output")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("preflight wrote artifact")
			}
			for _, c := range orkaCalls(t, dir) {
				if slices.Contains(c.Args, "create") && !slices.Contains(c.Args, "--dry-run=server") {
					t.Fatalf("mutated: %v", c.Args)
				}
			}
			if strings.Contains(fmt.Sprint(err)+diagnostics.String(), orkaTestToken()) {
				t.Fatal("private subprocess output leaked")
			}
		})
	}
}

func TestOrkaReadinessFailureStopsSubsequentCreates(t *testing.T) {
	for _, scenario := range []string{"replacement", "changed-generation", "create-race"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("accepted failed or replaced Provider")
			}
			for _, c := range orkaCalls(t, dir) {
				if c.Document != nil && !slices.Contains(c.Args, "--dry-run=server") && c.Document["kind"] != "Provider" {
					t.Fatalf("continued after failure: %+v", c)
				}
			}
		})
	}
}

func TestOrkaDryRunNeverMintsTokenOrWritesResources(t *testing.T) {
	a, opt, _, diagnostics, dir := orkaCreateFixture(t, "")
	opt.DryRun = true
	opt.Task = "Say hello"
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "create") && !slices.Contains(c.Args, "--dry-run=server") || slices.Contains(c.Args, "port-forward") {
			t.Fatalf("execution side effect: %v", c.Args)
		}
	}
	if !strings.Contains(diagnostics.String(), "result access and execution were not tested") {
		t.Fatal(diagnostics.String())
	}
}
