package kubectl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEvalKubectlHelper is the fake kubectl for evaluate. It serves the
// destination's kube-system UID, one live Agent from agent.json, the result
// account and Service, a token, a port-forward that only announces its bind
// (the test's own HTTP server owns the port), and Tasks: every create is
// logged and stored, and every read reports the phase KMX_EVAL_PHASE names.
func TestEvalKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_EVAL_DIR")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	fail := func() { fmt.Fprint(os.Stderr, "external failure "+orkaTestToken()); os.Exit(1) }
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	call := orkaCall{Args: args}
	if slices.Contains(args, "-f") {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(os.Stdin)
		_ = json.Unmarshal(raw.Bytes(), &call.Document)
	}
	log, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	if slices.Contains(args, "config") {
		server := "https://127.0.0.1:6443"
		if os.Getenv("KMX_EVAL_REMOTE") == "1" {
			server = "https://managed.example.invalid"
		}
		fmt.Printf(`{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":%q}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`, server)
		os.Exit(0)
	}
	if slices.Contains(args, "port-forward") {
		if os.Getenv("KMX_EVAL_STOCK_RELEASE") == "1" && !slices.Contains(args, "svc/orka") {
			fail()
		}
		if delay := os.Getenv("KMX_EVAL_FORWARD_DELAY"); delay != "" {
			pause, err := time.ParseDuration(delay)
			if err != nil {
				fail()
			}
			time.Sleep(pause)
		}
		_ = os.WriteFile(filepath.Join(dir, "forward-pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
		port, _, _ := strings.Cut(args[len(args)-1], ":")
		fmt.Println("Forwarding from 127.0.0.1:" + port + " -> 8080")
		time.Sleep(time.Hour)
	}
	if slices.Contains(args, "token") {
		fmt.Printf(`{"status":{"token":%q,"expirationTimestamp":%q}}`, orkaTestToken(), time.Now().Add(10*time.Minute).Format(time.RFC3339))
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 {
		kind, name := args[i+1], args[i+2]
		switch kind {
		case "namespace":
			fmt.Printf(`{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, getenvLiftTest("KMX_EVAL_CLUSTER_UID", "cluster-uid"))
		case "crd":
			plural, _, _ := strings.Cut(name, ".")
			raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_FIXTURES"), "v0.1.3", plural+".yaml"))
			if err != nil {
				fail()
			}
			_, _ = os.Stdout.Write(raw)
		case "serviceaccount":
			fmt.Print("serviceaccount/" + name)
		case "services":
			if raw := os.Getenv("KMX_EVAL_SERVICES"); raw != "" {
				fmt.Print(raw)
				break
			}
			fmt.Print(`{"items":[{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}]}`)
		case "service":
			if os.Getenv("KMX_EVAL_STOCK_RELEASE") == "1" && name != "orka" {
				fail()
			}
			fmt.Print(`{"spec":{"ports":[{"port":8080}]}}`)
		case "providers.core.orka.ai":
			raw, err := os.ReadFile(filepath.Join(dir, "provider.json"))
			if err != nil {
				os.Exit(0)
			}
			_, _ = os.Stdout.Write(raw)
		case "agents.core.orka.ai":
			raw, err := os.ReadFile(filepath.Join(dir, "agent.json"))
			if err != nil {
				os.Exit(0)
			}
			if swap := os.Getenv("KMX_EVAL_AGENT_SWAP_ON_READ"); swap != "" {
				countPath := filepath.Join(dir, "agent-read-count")
				countRaw, _ := os.ReadFile(countPath)
				count, _ := strconv.Atoi(string(countRaw))
				count++
				_ = os.WriteFile(countPath, []byte(strconv.Itoa(count)), 0600)
				threshold, _ := strconv.Atoi(swap)
				if count >= threshold {
					raw = bytes.Replace(raw, []byte(`"uid":"agent-uid"`), []byte(`"uid":"replacement-uid"`), 1)
				}
			}
			_, _ = os.Stdout.Write(raw)
		case "tasks.core.orka.ai":
			if delay := os.Getenv("KMX_EVAL_TASK_GET_DELAY"); delay != "" {
				pause, err := time.ParseDuration(delay)
				if err != nil {
					fail()
				}
				time.Sleep(pause)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "task-"+name+".json"))
			if err != nil {
				os.Exit(0)
			}
			var task map[string]any
			_ = json.Unmarshal(raw, &task)
			phase := getenvLiftTest("KMX_EVAL_PHASE", "Succeeded")
			if sequence := os.Getenv("KMX_EVAL_PHASE_SEQUENCE"); sequence != "" {
				path := filepath.Join(dir, "task-phase-read-count")
				countRaw, _ := os.ReadFile(path)
				count, _ := strconv.Atoi(string(countRaw))
				phases := strings.Split(sequence, ",")
				phase = phases[min(count, len(phases)-1)]
				_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0600)
			}
			if swap := os.Getenv("KMX_EVAL_TASK_SWAP_ON_READ"); swap != "" {
				threshold, _ := strconv.Atoi(swap)
				countRaw, _ := os.ReadFile(filepath.Join(dir, "task-phase-read-count"))
				count, _ := strconv.Atoi(string(countRaw))
				if count >= threshold {
					task["metadata"].(map[string]any)["uid"] = "replacement-task-uid"
				}
			}
			available := phase == "Succeeded" && os.Getenv("KMX_EVAL_RESULT_AVAILABLE") != "false"
			task["status"] = map[string]any{"phase": phase, "resultRef": map[string]any{"available": available}}
			_ = json.NewEncoder(os.Stdout).Encode(task)
		default:
			fail()
		}
		os.Exit(0)
	}
	if slices.Contains(args, "replace") && slices.Contains(args, "--dry-run=server") && call.Document != nil {
		_ = json.NewEncoder(os.Stdout).Encode(call.Document)
		os.Exit(0)
	}
	if slices.Contains(args, "create") && call.Document != nil && call.Document["kind"] == "Task" {
		if os.Getenv("KMX_EVAL_CREATE_FAIL") == "1" {
			fail()
		}
		meta := call.Document["metadata"].(map[string]any)
		meta["uid"] = "task-uid-" + meta["name"].(string)
		meta["generation"] = 1
		body, _ := json.Marshal(call.Document)
		_ = os.WriteFile(filepath.Join(dir, "task-"+meta["name"].(string)+".json"), body, 0600)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	fail()
}
