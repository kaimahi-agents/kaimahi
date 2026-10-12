package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Exercise the installed command route with real App/Prepare/Load/placement,
// native Tasks and result transport. Only kubectl and the remote API are fakes.
func TestWorkspaceEvaluateActualCLIPipeline(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	root := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("KMX_HOME", state)
	t.Setenv("KMX_WORKSPACE_CLI_FIXTURE", dir)
	t.Setenv("KMX_RECONCILE_DIR", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	fixturePath, e := filepath.Abs("../../internal/kmx/orkaschema/fixtures")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("KMX_RECONCILE_FIXTURES", fixturePath)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\nexec "+quoteShell(exe)+" -test.run=TestWorkspaceEvaluateKubectlHelper -- \"$@\"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	readFixtures := map[string]string{
		"deploy":                           `{"items":[{"kind":"Deployment","metadata":{"name":"orka-controller","namespace":"orka-system","uid":"controller-uid","generation":4,"labels":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller","helm.sh/chart":"orka-0.2.0","app.kubernetes.io/instance":"orka","app.kubernetes.io/managed-by":"Helm"}},"spec":{"replicas":1,"template":{"spec":{"serviceAccountName":"orka-controller","containers":[{"name":"controller","image":"ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370","args":["--controller-mode=harness-v2","--watch-namespace=orka-system","--ai-worker-service-account-name=orka-ai-worker","--ai-worker-image=ghcr.io/orka-agents/orka/ai-worker@sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4"]}]}}},"status":{"observedGeneration":4,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}`,
		"serviceaccounts":                  `{"items":[{"kind":"ServiceAccount","metadata":{"name":"orka-ai-worker","namespace":"orka-system","uid":"worker-uid","labels":{"orka.ai/worker":"true","orka.ai/worker-trust":"ai","helm.sh/chart":"orka-0.2.0","app.kubernetes.io/instance":"orka","app.kubernetes.io/name":"orka","app.kubernetes.io/managed-by":"Helm"}}}]}`,
		"serviceaccount-orka-ai-worker":    `{"kind":"ServiceAccount","metadata":{"name":"orka-ai-worker","namespace":"orka-system","uid":"worker-uid","labels":{"orka.ai/worker":"true","orka.ai/worker-trust":"ai","helm.sh/chart":"orka-0.2.0","app.kubernetes.io/instance":"orka","app.kubernetes.io/name":"orka","app.kubernetes.io/managed-by":"Helm"}}}`,
		"providers.core.orka.ai-inference": `{"kind":"Provider","metadata":{"name":"inference","namespace":"orka-system","uid":"inference-uid","generation":3,"resourceVersion":"9"},"spec":{"type":"openai","defaultModel":"WRONG_DEFAULT","secretRef":{"name":"target-secret","key":"api-key"}},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":3}]}}`,
		"secret-target-secret":             `{"kind":"Secret","metadata":{"name":"target-secret","namespace":"orka-system","uid":"secret-uid","resourceVersion":"7"},"data":{"api-key":"PRIVATE_SECRET_CANARY"}}`,
		"services":                         `{"items":[{"metadata":{"name":"orka","labels":{"app.kubernetes.io/name":"orka"}},"spec":{"selector":{"app.kubernetes.io/name":"orka","app.kubernetes.io/component":"controller"},"ports":[{"name":"api","port":8080}]}}]}`,
		"service-orka":                     `{"spec":{"ports":[{"port":8080}]}}`,
	}
	for name, body := range readFixtures {
		if e := os.WriteFile(filepath.Join(dir, "read-"+name+".json"), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer CLI_FIXTURE_BEARER" || r.URL.Query().Get("namespace") != "orka-system" {
			t.Error("wrong result authority")
			w.WriteHeader(403)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		if _, e := os.Stat(filepath.Join(dir, "task-"+name+".json")); os.IsNotExist(e) {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"result":"answer PRIVATE_ANSWER_CANARY"}`)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	input := filepath.Join(t.TempDir(), "instructions.md")
	if e := os.WriteFile(input, []byte("PACKAGED_INSTRUCTIONS_CANARY"), 0600); e != nil {
		t.Fatal(e)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
		*loads++
		if ctx != "kind-test" {
			t.Errorf("wrong config %q", ctx)
		}
		return &config.Config{KubeContext: ctx, ContextSource: config.SourceFlag}, nil
	}
	if e := execute([]string{"agent", "create", "sample", "--workspace", root, "--instructions", input, "--model", "packaged-model"}, deps); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte("id: cli-case\ninput: PACKAGED_INPUT_CANARY\nexpectContains: [answer]\n"), 0600); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	diagnostics.Reset()
	export := filepath.Join(t.TempDir(), "receipt.json")
	args := []string{"agent", "evaluate", "--workspace", root, "--to-context", "kind-test", "--to-namespace", "orka-system", "--inference", "provider:inference", "--out", export, "--result-port", port, "--case-timeout", "30s"}
	if e := execute(args, deps); e != nil {
		t.Fatalf("CLI evaluation: %v\n%s", e, diagnostics.String())
	}
	if *loads != 1 {
		t.Fatal("unexpected configuration loads")
	}
	raw, e := os.ReadFile(export)
	if e != nil {
		t.Fatal(e)
	}
	var r struct {
		Result                         string
		FullCaseSet, PromotionEligible bool
		Subject                        struct{ SuiteDigest, Model string }
		Cases                          []struct{ ID, TaskName, TaskUID string }
	}
	if e := json.Unmarshal(raw, &r); e != nil || r.Result != "pass" || !r.FullCaseSet || !r.PromotionEligible || r.Subject.Model != "packaged-model" || len(r.Cases) != 1 || r.Cases[0].TaskUID == "" {
		t.Fatalf("CLI receipt %s %v", raw, e)
	}
	for _, forbidden := range []string{"PRIVATE_ANSWER_CANARY", "PRIVATE_SECRET_CANARY", "CLI_FIXTURE_BEARER", "PACKAGED_INPUT_CANARY", "PACKAGED_INSTRUCTIONS_CANARY"} {
		if bytes.Contains(raw, []byte(forbidden)) || strings.Contains(out.String()+diagnostics.String(), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	taskRaw, e := os.ReadFile(filepath.Join(dir, "task-"+r.Cases[0].TaskName+".json"))
	if e != nil {
		t.Fatal(e)
	}
	var task struct {
		Spec struct {
			Type, Prompt string
			RuntimeRef   any
		}
	}
	_ = json.Unmarshal(taskRaw, &task)
	if task.Spec.Type != "ai" || task.Spec.Prompt != "PACKAGED_INPUT_CANARY" || task.Spec.RuntimeRef != nil {
		t.Fatal("wrong native Task")
	}
	// A fresh CLI invocation publishes the durable evaluated S without loading
	// target configuration.
	out.Reset()
	layout := filepath.Join(t.TempDir(), "layout")
	if e := execute([]string{"suite", "push", "--workspace", root, "--to-layout", layout, "team:v1"}, deps); e != nil || !strings.Contains(out.String(), r.Subject.SuiteDigest) {
		t.Fatalf("publish exact S: %v %s", e, &out)
	}
	if *loads != 1 {
		t.Fatal("publication loaded target config")
	}
}

func TestWorkspaceEvaluateKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_WORKSPACE_CLI_FIXTURE")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	finish := func(v any) {
		if json.NewEncoder(os.Stdout).Encode(v) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		os.Exit(1)
	}
	if slices.Contains(args, "port-forward") {
		port, _, _ := strings.Cut(args[len(args)-1], ":")
		fmt.Println("Forwarding from 127.0.0.1:" + port + " -> 8080")
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	if slices.Contains(args, "token") {
		finish(map[string]any{"status": map[string]any{"token": "CLI_FIXTURE_BEARER", "expirationTimestamp": time.Now().Add(10 * time.Minute).Format(time.RFC3339)}})
	}
	if slices.Contains(args, "rollout") {
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 && len(args) > i+2 {
		kind, name := args[i+1], args[i+2]
		if kind == "namespace" {
			finish(map[string]any{"kind": "Namespace", "metadata": map[string]any{"name": name, "uid": "uid-" + name}})
		}
		if kind == "serviceaccount" && name == "orka-result-reader" {
			fmt.Print("serviceaccount/" + name)
			os.Exit(0)
		}
		if kind == "crd" {
			if slices.Contains(args, "name") {
				fmt.Print("customresourcedefinition.apiextensions.k8s.io/" + name)
				os.Exit(0)
			}
			plural, _, _ := strings.Cut(name, ".")
			raw, e := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_FIXTURES"), "v0.2.0", plural+".yaml"))
			if e != nil {
				os.Exit(1)
			}
			_, _ = os.Stdout.Write(raw)
			os.Exit(0)
		}
		if kind == "tasks.core.orka.ai" {
			raw, e := os.ReadFile(filepath.Join(dir, "task-"+name+".json"))
			if os.IsNotExist(e) {
				os.Exit(0)
			}
			if e != nil {
				os.Exit(1)
			}
			var task map[string]any
			_ = json.Unmarshal(raw, &task)
			task["status"] = map[string]any{"phase": "Succeeded", "resultRef": map[string]any{"available": true}}
			finish(task)
		}
		key := kind
		if name != "-o" {
			key += "-" + name
		}
		if raw, e := os.ReadFile(filepath.Join(dir, "read-"+key+".json")); e == nil {
			output := args[slices.Index(args, "-o")+1]
			if strings.HasPrefix(output, "go-template=") {
				var value map[string]any
				_ = json.Unmarshal(raw, &value)
				p, e := template.New("projection").Parse(strings.TrimPrefix(output, "go-template="))
				if e != nil || p.Execute(os.Stdout, value) != nil {
					os.Exit(1)
				}
			} else {
				_, _ = os.Stdout.Write(raw)
			}
			os.Exit(0)
		}
	}
	raw, _ := io.ReadAll(os.Stdin)
	if slices.Contains(args, "create") && slices.Contains(args, "-f") && !slices.Contains(args, "--dry-run=server") {
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		if doc["kind"] == "Task" {
			meta := doc["metadata"].(map[string]any)
			name := meta["name"].(string)
			meta["uid"], meta["generation"], meta["resourceVersion"] = "task-uid-"+name, 1, "1"
			body, _ := json.Marshal(doc)
			if os.WriteFile(filepath.Join(dir, "task-"+name+".json"), body, 0600) != nil {
				os.Exit(1)
			}
			finish(doc)
		}
	}
	e := kubectlfake.Reconcile(kubectlfake.Request{Context: context.Background(), Args: args, Getenv: os.Getenv, Stdin: bytes.NewReader(raw), Stdout: os.Stdout, Stderr: os.Stderr})
	if e != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
