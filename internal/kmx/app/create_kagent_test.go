package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

type kagentCall struct {
	Args []string       `json:"args"`
	Body map[string]any `json:"body,omitempty"`
}

// TestKagentKubectlHelper is the fake executable boundary. It is re-executed
// as kubectl, so stdin, context pinning, order, and one-shot A2A behavior all
// exercise the production subprocess code.
func TestKagentKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_KAGENT_TEST_DIR")
	if dir == "" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		os.Exit(2)
	}
	args := os.Args[separator+1:]
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	stdin, _ := io.ReadAll(os.Stdin)
	call := kagentCall{Args: args}
	if len(bytes.TrimSpace(stdin)) > 0 {
		_ = json.Unmarshal(stdin, &call.Body)
	}
	log, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	fail := func() {
		fmt.Fprint(os.Stderr, "external Kagent test failure")
		os.Exit(1)
	}
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	scenario := os.Getenv("KMX_KAGENT_TEST_SCENARIO")
	if slices.Contains(args, "config") {
		fmt.Print(`{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`)
		os.Exit(0)
	}
	if rawIndex := slices.Index(args, "--raw"); rawIndex >= 0 {
		path := args[rawIndex+1]
		if strings.HasSuffix(path, "/version") {
			version := "v0.10.2"
			commit := kagentPinnedCommit
			if scenario == "wrong-version" {
				version = "v0.10.1"
				commit = "1234567"
			}
			fmt.Printf(`{"kagent_version":%q,"git_commit":%q}`, version, commit)
			os.Exit(0)
		}
		if strings.HasSuffix(path, "/.well-known/agent-card.json") {
			if scenario == "card-stale" {
				fail()
			}
			if scenario == "card-lag" {
				marker := filepath.Join(dir, "card-attempt")
				if _, err := os.Stat(marker); os.IsNotExist(err) {
					_ = os.WriteFile(marker, []byte("attempted"), 0o600)
					fail()
				}
			}
			fmt.Print(`{"name":"sample","capabilities":{"streaming":true},"defaultInputModes":["text"],"defaultOutputModes":["text"]}`)
			os.Exit(0)
		}
		if call.Body["method"] != "message/send" || !strings.Contains(path, "/api/a2a/agents/sample") {
			fail()
		}
		if scenario == "task-timeout" {
			_ = os.WriteFile(filepath.Join(dir, "message-attempted"), []byte("attempted"), 0o600)
			time.Sleep(5 * time.Second)
			fail()
		}
		id, _ := call.Body["id"].(string)
		_ = os.WriteFile(filepath.Join(dir, "message-sent"), []byte("sent"), 0o600)
		if scenario == "wrong-rpc-id" {
			id = "other"
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"task-1","contextId":"context-1","status":{"state":"completed","message":{"kind":"message","role":"agent","messageId":"answer-1","contextId":"context-1","parts":[{"kind":"text","text":"one answer"}]}}}}`, id)
		os.Exit(0)
	}
	installCount := func() int {
		raw, err := os.ReadFile(filepath.Join(dir, "install-count"))
		if err != nil {
			return 0
		}
		var count int
		_, _ = fmt.Sscanf(string(raw), "%d", &count)
		return count
	}
	markInstallRead := func() int {
		count := installCount() + 1
		_ = os.WriteFile(filepath.Join(dir, "install-count"), []byte(fmt.Sprint(count)), 0o600)
		return count
	}
	if slices.Contains(args, "auth") && slices.Contains(args, "can-i") {
		denied := map[string][2]string{
			"rbac-denied":              {"create", "deployments.apps"},
			"rbac-agent-list-denied":   {"list", "agents.kagent.dev"},
			"rbac-model-watch-denied":  {"watch", "modelconfigs.kagent.dev"},
			"rbac-model-status-denied": {"update", "modelconfigs.kagent.dev/status"},
		}
		want, deny := denied[scenario]
		if deny && slices.Contains(args, want[0]) && slices.Contains(args, want[1]) {
			fmt.Print("no\n")
		} else {
			fmt.Print("yes\n")
		}
		os.Exit(0)
	}
	if getIndex := slices.Index(args, "get"); getIndex >= 0 {
		kind := args[getIndex+1]
		switch kind {
		case "services":
			if !slices.Contains(args, "--all-namespaces") {
				name := args[getIndex+2]
				if name != "sample" {
					fail()
				}
				if scenario == "child-service-collision" {
					fmt.Print("present")
				}
				os.Exit(0)
			}
			if scenario == "slow-preflight" {
				time.Sleep(5 * time.Second)
				fail()
			}
			count := markInstallRead()
			imageTag := "0.10.2"
			if scenario == "wrong-label" {
				imageTag = "0.10.1"
			}
			resourceVersion := "5"
			if scenario == "controller-replaced" && count > 1 {
				resourceVersion = "replacement"
			}
			if scenario == "final-platform-drift" {
				if _, err := os.Stat(filepath.Join(dir, "sample-agents.kagent.dev.json")); err == nil {
					resourceVersion = "replacement"
				}
			}
			fmt.Printf(`{"items":[{"metadata":{"name":"kagent-controller-metrics","namespace":"kagent","uid":"metrics-service-uid","resourceVersion":"4","labels":{"app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"selector":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"},"ports":[{"name":"https","protocol":"TCP","port":8443}]}},{"metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-service-uid","resourceVersion":%q,"labels":{"app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller","app.kubernetes.io/version":%q,"helm.sh/chart":"kagent-0.10.2"}},"spec":{"selector":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"},"ports":[{"name":"controller","protocol":"TCP","port":8083}]}}]}`, resourceVersion, imageTag)
		case "deployments":
			image := kagentControllerImage
			authMode := "unsecure"
			commandOverride := ""
			if scenario == "wrong-auth" {
				authMode = "other"
			}
			if scenario == "wrong-controller-image" {
				image = kagentControllerImageRepo + ":0.10." + "1"
			}
			if scenario == "trusted-proxy" {
				authMode = "trusted-proxy"
			}
			if scenario == "controller-args" {
				commandOverride = `,"args":["--image-tag=latest"]`
			}
			fmt.Printf(`{"items":[{"kind":"Deployment","metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-deployment-uid","resourceVersion":"6","generation":1,"labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller","app.kubernetes.io/version":"0.10.2","helm.sh/chart":"kagent-0.10.2"}},"spec":{"replicas":1,"selector":{"matchLabels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"template":{"metadata":{"labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"serviceAccountName":"kagent-controller","containers":[{"name":"controller","image":%q%s,"env":[{"name":"AUTH_MODE","value":%q}],"envFrom":[{"configMapRef":{"name":"kagent-controller"}}]}]}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}`, image, commandOverride, authMode)
		case "deployments.apps":
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-deployment-collision" {
				fmt.Print("present")
			}
		case "deployment":
			name := args[getIndex+2]
			if name != "sample" {
				fail()
			}
			image := kagentGoImage
			if os.Getenv("KMX_KAGENT_TEST_RUNTIME") == "python" {
				image = kagentPythonImage
			}
			uid, resourceVersion := "agent-deployment-uid", "20"
			if scenario == "workload-replaced" {
				if _, err := os.Stat(filepath.Join(dir, "message-sent")); err == nil {
					uid, resourceVersion = "replacement-deployment-uid", "21"
				}
			}
			fmt.Printf(`{"kind":"Deployment","metadata":{"name":"sample","namespace":"agents","uid":%q,"resourceVersion":%q,"generation":1,"labels":{"app":"kagent","kagent":"sample"},"ownerReferences":[{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","name":"sample","uid":"agent-uid","controller":true}]},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"kagent","kagent":"sample"}},"template":{"metadata":{"labels":{"app":"kagent","kagent":"sample"}},"spec":{"containers":[{"name":"kagent","image":%q}]}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}`, uid, resourceVersion, image)
		case "pods":
			selectorIndex := slices.Index(args, "-l")
			if selectorIndex < 0 {
				fail()
			}
			selector := args[selectorIndex+1]
			if strings.Contains(selector, "app.kubernetes.io/component=controller") {
				imageID := "containerd://" + kagentControllerImageRepo + "@sha256:" + kagentControllerAMD64Digest
				if scenario == "wrong-controller-imageid" {
					imageID = "containerd://" + kagentControllerImageRepo + "@sha256:bad"
				}
				fmt.Printf(`{"items":[{"metadata":{"name":"kagent-controller-pod","namespace":"kagent","uid":"controller-pod-uid","resourceVersion":"7","labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"nodeName":"node-1","containers":[{"name":"controller","image":%q}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"controller","image":%q,"imageID":%q,"ready":true}]}}]}`, kagentControllerImage, kagentControllerImage, imageID)
			} else {
				image, repository, digest := kagentGoImage, kagentGoImageRepo, kagentGoAMD64Digest
				if os.Getenv("KMX_KAGENT_TEST_RUNTIME") == "python" {
					image, repository, digest = kagentPythonImage, kagentPythonImageRepo, kagentPythonAMD64Digest
				}
				if scenario == "wrong-agent-imageid" {
					digest = "bad"
				}
				fmt.Printf(`{"items":[{"metadata":{"name":"sample-pod","namespace":"agents","uid":"agent-pod-uid","resourceVersion":"22","labels":{"app":"kagent","kagent":"sample"}},"spec":{"nodeName":"node-1","containers":[{"name":"kagent","image":%q}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"kagent","image":%q,"imageID":%q,"ready":true}]}}]}`, image, image, "containerd://"+repository+"@sha256:"+digest)
			}
		case "configmap":
			tag := "0.10.2"
			if scenario == "wrong-image-config" {
				tag = "latest"
			}
			watchNamespaces := ""
			if scenario == "excluded-namespace" {
				watchNamespaces = "other,more"
			}
			fmt.Printf(`{"kind":"ConfigMap","metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-config-uid","resourceVersion":"8","labels":{"app.kubernetes.io/version":"0.10.2","helm.sh/chart":"kagent-0.10.2"}},"data":{"IMAGE_REGISTRY":"ghcr.io","IMAGE_REPOSITORY":"kagent-dev/kagent/app","IMAGE_TAG":%q,"GO_IMAGE_REGISTRY":"ghcr.io","GO_IMAGE_REPOSITORY":"kagent-dev/kagent/golang-adk","GO_IMAGE_TAG":%q,"WATCH_NAMESPACES":%q}}`, tag, tag, watchNamespaces)
		case "crd":
			name := args[getIndex+2]
			kindName, plural := "Agent", "agents"
			if name == "modelconfigs.kagent.dev" {
				kindName, plural = "ModelConfig", "modelconfigs"
			} else if name != "agents.kagent.dev" {
				fail()
			}
			schemaType := "object"
			if scenario == "wrong-schema" && name == "agents.kagent.dev" {
				schemaType = "string"
			}
			fmt.Printf(`{"metadata":{"name":%q},"spec":{"group":"kagent.dev","scope":"Namespaced","names":{"kind":%q,"plural":%q},"versions":[{"name":"v1alpha1","served":true,"storage":false},{"name":"v1alpha2","served":true,"storage":true,"schema":{"openAPIV3Schema":{"type":%q}}}]}}`, name, kindName, plural, schemaType)
		case "namespace":
			if args[getIndex+2] != "kube-system" {
				fail()
			}
			uid := "cluster-uid"
			if scenario == "final-cluster-drift" {
				if _, err := os.Stat(filepath.Join(dir, "sample-agents.kagent.dev.json")); err == nil {
					uid = "replacement-cluster-uid"
				}
			}
			fmt.Printf(`{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid)
		case "service":
			if args[getIndex+2] != "sample" {
				fail()
			}
			fmt.Print(`{"kind":"Service","metadata":{"name":"sample","namespace":"agents","uid":"agent-service-uid","resourceVersion":"23","ownerReferences":[{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","name":"sample","uid":"agent-uid","controller":true}]},"spec":{"selector":{"app":"kagent","kagent":"sample"},"ports":[{"name":"http","protocol":"TCP","port":8080}]}}`)
		case "secret":
			name := args[getIndex+2]
			if name == "sample" {
				if scenario == "child-secret-collision" || scenario == "model-secret-same-name" {
					fmt.Print("present")
				}
				os.Exit(0)
			}
			fmt.Print("secret\npresent")
		case "secrets":
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-secret-collision" || scenario == "model-secret-same-name" {
				fmt.Print("present")
			}
		case "serviceaccounts":
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-serviceaccount-collision" {
				fmt.Print("present")
			}
		case "remotemcpservers.kagent.dev":
			name := args[getIndex+2]
			uid, secretHash, endpoint := "tool-uid", "tool-secret-hash", "https://tools.example.invalid/mcp"
			discovered := `[{"name":"read"},{"name":"list"}]`
			if _, err := os.Stat(filepath.Join(dir, "sample-modelconfigs.kagent.dev.json")); err == nil {
				switch scenario {
				case "tool-replaced":
					uid = "replacement-tool-uid"
				case "tool-spec-drift":
					endpoint = "https://changed.example.invalid/mcp"
				case "tool-secret-drift":
					secretHash = "changed-tool-secret-hash"
				case "tool-set-drift":
					discovered = `[{"name":"read"},{"name":"changed"}]`
				}
			}
			fmt.Printf(`{"apiVersion":"kagent.dev/v1alpha2","kind":"RemoteMCPServer","metadata":{"name":%q,"namespace":"agents","uid":%q,"generation":3},"spec":{"description":"tools","protocol":"STREAMABLE_HTTP","url":%q},"status":{"observedGeneration":3,"conditions":[{"type":"Accepted","status":"True","observedGeneration":3}],"secretHash":%q,"discoveredTools":%s}}`, name, uid, endpoint, secretHash, discovered)
		case "modelconfigs.kagent.dev", "agents.kagent.dev":
			name := args[getIndex+2]
			if slices.Contains(args, "--ignore-not-found=true") {
				if scenario == "model-collision" && kind == "modelconfigs.kagent.dev" {
					fmt.Print("present")
				}
				os.Exit(0)
			}
			stored, err := os.ReadFile(filepath.Join(dir, name+"-"+kind+".json"))
			if err != nil {
				fail()
			}
			var object map[string]any
			if json.Unmarshal(stored, &object) != nil {
				fail()
			}
			objectKind := object["kind"].(string)
			generation := int64(1)
			observed := generation
			conditionGeneration := generation
			if scenario == "stale-agent" && objectKind == "Agent" {
				observed, conditionGeneration = 0, 0
			}
			conditions := []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": conditionGeneration}}
			if objectKind == "Agent" {
				conditions = append(conditions, map[string]any{"type": "Ready", "status": "True", "reason": "DeploymentReady", "observedGeneration": conditionGeneration})
			} else {
				// Exact v0.10.2 stamps ModelConfig.status.observedGeneration,
				// but not Accepted.observedGeneration.
				conditions = []any{map[string]any{"type": "Accepted", "status": "True"}}
			}
			status := map[string]any{"observedGeneration": observed, "conditions": conditions}
			if objectKind == "ModelConfig" {
				status["secretHash"] = "model-secret-hash"
			}
			if scenario == "later-live-mutated" && objectKind == "Agent" {
				object["spec"].(map[string]any)["description"] = "mutated"
			}
			object["status"] = status
			_ = json.NewEncoder(os.Stdout).Encode(object)
		default:
			fail()
		}
		os.Exit(0)
	}
	if slices.Contains(args, "create") {
		if call.Body == nil {
			fail()
		}
		if slices.Contains(args, "--dry-run=server") {
			body := call.Body
			if body["kind"] == "ModelConfig" {
				spec := body["spec"].(map[string]any)
				if openAI, ok := spec["openAI"].(map[string]any); ok {
					openAI["apiFormat"] = "chatCompletions"
				}
			}
			if scenario == "dry-run-mutated" && body["kind"] == "Agent" {
				body["spec"].(map[string]any)["description"] = "mutated"
			}
			_ = json.NewEncoder(os.Stdout).Encode(body)
			os.Exit(0)
		}
		kind, _ := call.Body["kind"].(string)
		if kind != "ModelConfig" && kind != "Agent" {
			fail()
		}
		metadata := call.Body["metadata"].(map[string]any)
		metadata["uid"] = strings.ToLower(kind) + "-uid"
		metadata["generation"] = 1
		if kind == "ModelConfig" {
			spec := call.Body["spec"].(map[string]any)
			if openAI, ok := spec["openAI"].(map[string]any); ok {
				openAI["apiFormat"] = "chatCompletions"
			}
		}
		if scenario == "create-response-mutated" && kind == "Agent" {
			call.Body["spec"].(map[string]any)["description"] = "mutated"
		}
		body, _ := json.Marshal(call.Body)
		name := metadata["name"].(string)
		plural := kagentPlural(kind)
		if err := os.WriteFile(filepath.Join(dir, name+"-"+plural+".json"), body, 0o600); err != nil {
			fail()
		}
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	fail()
}

func kagentCreateFixture(t *testing.T, scenario string) (*App, CreateOptions, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", "exec "+shellArg(exe)+" -test.run=^TestKagentKubectlHelper$ -- \"$@\"")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("KMX_KAGENT_TEST_DIR", dir)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
	t.Setenv("KMX_KAGENT_TEST_RUNTIME", "go")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	oldAgentSchema, oldModelSchema, oldPoll := kagentAgentSchemaSHA256, kagentModelSchemaSHA256, kagentPollInterval
	oldPreflight, oldReadiness := kagentPreflightTimeout, kagentReadinessTimeout
	oldCard, oldTask := kagentAgentCardTimeout, kagentTaskTimeout
	kagentAgentSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(`{"type":"object"}`)))
	kagentModelSchemaSHA256 = kagentAgentSchemaSHA256
	kagentPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		kagentAgentSchemaSHA256, kagentModelSchemaSHA256, kagentPollInterval = oldAgentSchema, oldModelSchema, oldPoll
		kagentPreflightTimeout, kagentReadinessTimeout = oldPreflight, oldReadiness
		kagentAgentCardTimeout, kagentTaskTimeout = oldCard, oldTask
	})
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	runner := &run.Runner{Stdout: out, Stderr: diagnostics, Echo: true}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: runner, Out: out, Err: diagnostics}
	opt := CreateOptions{
		Runtime: "kagent", KagentRuntime: "go", Name: "sample", Namespace: "agents",
		Description: "Sample Kagent agent", ProviderType: "openai", Model: "gpt-4o-mini",
		Secret: "model-key", SecretKey: "api-key", InstructionText: "Answer briefly.",
		Out: filepath.Join(dir, "sample.yaml"), BundlePath: filepath.Join(dir, "agents", "sample"),
	}
	return a, opt, out, diagnostics, dir
}

func kagentCalls(t *testing.T, dir string) []kagentCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []kagentCall
	decoder := json.NewDecoder(f)
	for {
		var call kagentCall
		if err := decoder.Decode(&call); err == io.EOF {
			return calls
		} else if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
}

func assertKagentNoClusterWrites(t *testing.T, dir string) {
	t.Helper()
	for _, call := range kagentCalls(t, dir) {
		if call.Body != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("refusal reached a cluster write: %+v", call)
		}
	}
}

func assertKagentNoTaskOrReceipt(t *testing.T, opt CreateOptions, dir string) {
	t.Helper()
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			t.Fatalf("refusal sent a task: %+v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote a receipt: %v", err)
	}
}

func kagentOfflineOptions(out, bundle string) CreateOptions {
	return CreateOptions{
		Runtime: "kagent", KagentRuntime: "go", Name: "sample", Namespace: "agents",
		Description: "Sample Kagent agent", ProviderType: "openai", Model: "gpt-4o-mini",
		Secret: "model-key", SecretKey: "api-key", InstructionText: "Answer briefly.",
		Out: out, BundlePath: bundle, NoApply: true,
	}
}

func TestCreateAgentDefaultAndExplicitOrkaAreEquivalent(t *testing.T) {
	create := func(runtimeName string) (string, string) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		a := &App{Out: &out, Err: &diagnostics}
		opt := goldenNoTaskCreate("-")
		opt.Runtime = runtimeName
		opt.BundlePath = filepath.Join(t.TempDir(), "bundle")
		if err := a.CreateAgent(opt); err != nil {
			t.Fatal(err)
		}
		return out.String(), diagnostics.String()
	}
	implicitOut, implicitDiagnostics := create("")
	explicitOut, explicitDiagnostics := create("orka")
	if implicitOut != explicitOut || implicitDiagnostics != explicitDiagnostics {
		t.Fatal("explicit Orka changed existing output bytes or diagnostics")
	}
}

func TestCreateAgentUnknownRuntimeDoesNoFileOrClusterWork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	artifact := filepath.Join(dir, "artifact.yaml")
	err := a.CreateAgent(CreateOptions{Runtime: "other", Instructions: filepath.Join(dir, "missing"), Out: artifact})
	if err == nil || !strings.Contains(err.Error(), "unsupported agent runtime") {
		t.Fatalf("unknown runtime error = %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("unknown runtime touched artifact: %v", err)
	}
	if out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatal("unknown runtime emitted output")
	}
}

func TestCreateAgentDefaultAndExplicitOrkaMakeEquivalentCalls(t *testing.T) {
	implicitApp, implicitOpt, _, _, implicitDir := orkaCreateFixture(t, "")
	implicitOpt.Runtime = ""
	if err := implicitApp.CreateAgent(implicitOpt); err != nil {
		t.Fatal(err)
	}
	implicit := orkaCalls(t, implicitDir)

	explicitApp, explicitOpt, _, _, explicitDir := orkaCreateFixture(t, "")
	explicitOpt.Runtime = "orka"
	if err := explicitApp.CreateAgent(explicitOpt); err != nil {
		t.Fatal(err)
	}
	explicit := orkaCalls(t, explicitDir)
	if !reflect.DeepEqual(implicit, explicit) {
		t.Fatalf("explicit Orka changed kubectl calls:\nimplicit=%+v\nexplicit=%+v", implicit, explicit)
	}
}

func TestKagentOfflineArtifactAndPortableBundleNeedNoPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	bundlePath := filepath.Join(t.TempDir(), "agents", "sample")
	opt := kagentOfflineOptions("-", bundlePath)
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	wantBundle, err := scaffold.GenerateKagent(scaffold.KagentSpec{
		Name: "sample", Namespace: "agents", Description: "Sample Kagent agent",
		Runtime: "go", Instructions: "Answer briefly.", ProviderType: "openai",
		Model: "gpt-4o-mini", SecretName: "model-key", SecretKey: "api-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := wantBundle.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Fatalf("offline artifact differs from scaffold.KagentArtifact:\n%s", out.String())
	}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		if _, err := os.Stat(filepath.Join(bundlePath, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(bundlePath, agentruntime.EvaluationCaseDir, "example.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Kagent bundle created unsupported evaluation example: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(bundlePath, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	portable, err := agentruntime.ParsePortableAgent(source)
	if err != nil || portable.Extensions.Kagent == nil || portable.Extensions.Orka != nil {
		t.Fatalf("stored source is not a Kagent portable agent: %+v, %v", portable, err)
	}
	bindings, err := os.ReadFile(filepath.Join(bundlePath, "bindings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentruntime.ParseKagentBindings(bindings); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("identical Kagent bundle retry failed: %v", err)
	}
}

func TestKagentOfflineRefusesModelSecretWithAgentName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	root := t.TempDir()
	opt := kagentOfflineOptions("-", filepath.Join(root, "bundle"))
	opt.Secret = opt.Name
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "model Secret name must differ") {
		t.Fatalf("same-name Secret error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("same-name Secret refusal emitted an artifact")
	}
	if _, err := os.Stat(opt.BundlePath); !os.IsNotExist(err) {
		t.Fatalf("same-name Secret refusal wrote a bundle: %v", err)
	}
}

func TestKagentPythonOnlineProvesOfficialRuntimeWorkload(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "")
	opt.KagentRuntime = "python"
	t.Setenv("KMX_KAGENT_TEST_RUNTIME", "python")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	foundConfig, foundWorkload := false, false
	for _, call := range kagentCalls(t, dir) {
		if slices.Contains(call.Args, "configmap") {
			foundConfig = true
		}
		if slices.Contains(call.Args, "pods") && slices.Contains(call.Args, "app=kagent,kagent=sample") {
			foundWorkload = true
		}
	}
	if !foundConfig || !foundWorkload {
		t.Fatalf("python proof calls: config=%v workload=%v", foundConfig, foundWorkload)
	}
}

func TestKagentCreateRefusesUnsupportedFlagsBeforeOutput(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*CreateOptions)
	}{
		{"skills", func(o *CreateOptions) { o.Skills = "summarize" }},
		{"agent requests", func(o *CreateOptions) { o.AgentRequestsPerMinute = "1" }},
		{"agent tokens", func(o *CreateOptions) { o.AgentTokensPerMinute = "1" }},
		{"provider requests", func(o *CreateOptions) { o.ProviderRequestsPerMinute = "1" }},
		{"provider tokens", func(o *CreateOptions) { o.ProviderTokensPerMinute = "1" }},
		{"schema", func(o *CreateOptions) { o.SchemaTarget = "v0.2.0" }},
		{"coordination", func(o *CreateOptions) { o.Coordination = true }},
		{"allowed Agent", func(o *CreateOptions) { o.AllowedAgents = []string{"helper"} }},
		{"Azure deployment", func(o *CreateOptions) { o.AzureDeployment = "chat-prod" }},
		{"Azure API version", func(o *CreateOptions) { o.AzureAPIVersion = "2024-10-21" }},
		{"result account", func(o *CreateOptions) { o.ResultServiceAccount = "reader" }},
		{"multiple tool bindings", func(o *CreateOptions) { o.Tools = "one:read,two:list" }},
		{"task offline", func(o *CreateOptions) { o.Task = "private prompt" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			opt := kagentOfflineOptions("-", "")
			change.edit(&opt)
			if err := a.CreateAgent(opt); err == nil {
				t.Fatal("unsupported Kagent option was accepted")
			}
			if out.Len() != 0 {
				t.Fatal("invalid Kagent options emitted an artifact")
			}
		})
	}
}

func TestKagentExactVersionMismatchStopsBeforeAdmissionOrWrites(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "wrong-version")
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "exact v0.10.2") || !strings.Contains(err.Error(), "no Kubernetes resources were changed") {
		t.Fatalf("wrong version error = %v", err)
	}
	for _, call := range kagentCalls(t, dir) {
		if call.Body != nil {
			t.Fatalf("version mismatch reached admission/write: %+v", call)
		}
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatalf("version mismatch emitted artifact: %v", err)
	}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		if _, err := os.Stat(filepath.Join(opt.BundlePath, name)); err != nil {
			t.Fatalf("failed online deployment did not preserve portable bundle: %v", err)
		}
	}
}

func TestKagentPreflightTimeoutStopsBeforeWrites(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "slow-preflight")
	kagentPreflightTimeout = 50 * time.Millisecond
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "no Kubernetes resources were changed") {
		t.Fatalf("preflight timeout error = %v", err)
	}
	assertKagentNoClusterWrites(t, dir)
	assertKagentNoTaskOrReceipt(t, opt, dir)
}

func TestKagentOnlineDependencyAndWriteOrder(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, call := range kagentCalls(t, dir) {
		if call.Body == nil || call.Body["kind"] == nil {
			continue
		}
		kind := call.Body["kind"].(string)
		if slices.Contains(call.Args, "--dry-run=server") {
			order = append(order, "dry-"+kind)
		} else {
			order = append(order, "create-"+kind)
		}
	}
	want := []string{"dry-ModelConfig", "dry-Agent", "create-ModelConfig", "create-Agent"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("Kagent admission/write order = %v, want %v", order, want)
	}
	calls := kagentCalls(t, dir)
	index := func(match func(kagentCall) bool) int {
		for i, call := range calls {
			if match(call) {
				return i
			}
		}
		return -1
	}
	secretRead := index(func(call kagentCall) bool { return slices.Contains(call.Args, "secret") })
	modelDryRun := index(func(call kagentCall) bool {
		return call.Body["kind"] == "ModelConfig" && slices.Contains(call.Args, "--dry-run=server")
	})
	modelCreate := index(func(call kagentCall) bool {
		return call.Body["kind"] == "ModelConfig" && !slices.Contains(call.Args, "--dry-run=server")
	})
	agentCreate := index(func(call kagentCall) bool {
		return call.Body["kind"] == "Agent" && !slices.Contains(call.Args, "--dry-run=server")
	})
	modelReadyRead := -1
	for i := modelCreate + 1; i >= 0 && i < len(calls); i++ {
		if slices.Contains(calls[i].Args, "get") && slices.Contains(calls[i].Args, "modelconfigs.kagent.dev") && slices.Contains(calls[i].Args, "json") {
			modelReadyRead = i
			break
		}
	}
	if secretRead < 0 || !(secretRead < modelDryRun && modelDryRun < modelCreate && modelCreate < modelReadyRead && modelReadyRead < agentCreate) {
		t.Fatalf("dependency order not enforced: secret=%d model-dry=%d model-create=%d model-ready=%d agent-create=%d", secretRead, modelDryRun, modelCreate, modelReadyRead, agentCreate)
	}
	artifact, err := os.ReadFile(opt.Out)
	if err != nil || !strings.Contains(string(artifact), "exact Kagent v0.10.2") {
		t.Fatalf("online artifact missing: %v\n%s", err, artifact)
	}
	if !strings.Contains(string(artifact), opt.InstructionText) {
		t.Fatal("review artifact omitted the system instructions")
	}
	if strings.Contains(string(artifact), "kaimahi.dev/bundle") || strings.Contains(string(artifact), "portable-digest") {
		t.Fatal("online artifact contains write-only ownership annotations")
	}
	for _, call := range kagentCalls(t, dir) {
		kind, _ := call.Body["kind"].(string)
		if kind != "ModelConfig" && kind != "Agent" {
			continue
		}
		metadata := call.Body["metadata"].(map[string]any)
		annotations := metadata["annotations"].(map[string]any)
		for _, name := range []string{"kaimahi.dev/bundle", "kaimahi.dev/portable-digest", "kaimahi.dev/rendered-digest"} {
			if strings.TrimSpace(fmt.Sprint(annotations[name])) == "" {
				t.Fatalf("%s payload lacks %s", kind, name)
			}
		}
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundlePath, "receipts"))
	if err != nil || len(receipts) != 1 || !strings.HasPrefix(receipts[0].Name(), "create-") {
		t.Fatalf("Kagent create receipt = %v, %v", receipts, err)
	}
	raw, err := os.ReadFile(filepath.Join(opt.BundlePath, "receipts", receipts[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var receipt kagentCreateReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.APIVersion != "kmx.kaimahi.dev/v1alpha1" ||
		receipt.Kind != "KagentCreateReceipt" || receipt.ClusterUID != "cluster-uid" || receipt.AgentRef.UID != "agent-uid" ||
		len(receipt.Deploy.Resources) != 2 {
		t.Fatalf("invalid Kagent create receipt: %s", raw)
	}
	if opt.Task != "" && strings.Contains(string(raw), opt.Task) || strings.Contains(string(raw), "one answer") || strings.Contains(string(raw), opt.InstructionText) {
		t.Fatal("Kagent create receipt contains prompt, answer, or instructions")
	}
	info, err := os.Stat(filepath.Join(opt.BundlePath, "receipts", receipts[0].Name()))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Kagent create receipt mode = %v, %v", info, err)
	}
}

func TestKagentPhaseTimeoutsDoNotConsumeTheOperationContext(t *testing.T) {
	operationCtx, cancelOperation := context.WithCancel(t.Context())
	defer cancelOperation()
	preflightCtx, cancelPreflight := kagentPhaseContext(operationCtx, time.Nanosecond)
	defer cancelPreflight()
	<-preflightCtx.Done()
	if !errors.Is(preflightCtx.Err(), context.DeadlineExceeded) || operationCtx.Err() != nil {
		t.Fatalf("preflight timeout changed operation context: preflight=%v operation=%v", preflightCtx.Err(), operationCtx.Err())
	}
	readinessCtx, cancelReadiness := kagentPhaseContext(operationCtx, time.Second)
	defer cancelReadiness()
	if readinessCtx.Err() != nil {
		t.Fatalf("fresh readiness phase inherited preflight timeout: %v", readinessCtx.Err())
	}
}

func TestKagentTaskPhasesHaveIndependentTimeouts(t *testing.T) {
	for _, tc := range []struct {
		scenario, want string
	}{
		{"card-stale", "agent card"},
		{"task-timeout", "may have executed"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, tc.scenario)
			opt.Task = "private prompt"
			if tc.scenario == "card-stale" {
				kagentAgentCardTimeout = 50 * time.Millisecond
			} else {
				kagentTaskTimeout = 2 * time.Second
			}
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), opt.Task) {
				t.Fatalf("phase timeout error = %v", err)
			}
			if tc.scenario == "card-stale" {
				assertKagentNoTaskOrReceipt(t, opt, dir)
			} else {
				if _, statErr := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(statErr) {
					t.Fatalf("ambiguous task wrote receipt: %v", statErr)
				}
				if _, statErr := os.Stat(filepath.Join(dir, "message-attempted")); statErr != nil {
					t.Fatalf("task timeout happened before message/send started: %v", statErr)
				}
				sends := 0
				for _, call := range kagentCalls(t, dir) {
					if call.Body["method"] == "message/send" {
						sends++
					}
				}
				if sends != 1 {
					t.Fatalf("ambiguous task sends = %d, want 1", sends)
				}
			}
		})
	}
}

func TestKagentInstallationProofRejectsDriftAndAllowsMetricsService(t *testing.T) {
	for _, tc := range []struct {
		scenario, want string
	}{
		{"wrong-schema", "openAPIV3Schema"},
		{"wrong-controller-image", "official image"},
		{"controller-args", "command or arguments"},
		{"wrong-controller-imageid", "published v0.10.2 digest"},
		{"wrong-image-config", "image config"},
		{"wrong-auth", "AUTH_MODE"},
		{"wrong-label", "app.kubernetes.io/version"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, tc.scenario)
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			for _, call := range kagentCalls(t, dir) {
				if call.Body != nil {
					t.Fatalf("installation drift reached admission/write: %+v", call)
				}
			}
			if _, err := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(err) {
				t.Fatalf("failed create wrote receipt: %v", err)
			}
		})
	}
	a, opt, _, _, _ := kagentCreateFixture(t, "")
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("same-labeled metrics Service was not tolerated: %v", err)
	}
}

func TestKagentControllerReplacementRaceStopsBeforeWrites(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "controller-replaced")
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "changed during preflight") || !strings.Contains(err.Error(), "no Kubernetes resources were changed") {
		t.Fatalf("replacement race error = %v", err)
	}
	for _, call := range kagentCalls(t, dir) {
		if call.Body != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("controller replacement reached a cluster write: %+v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("replacement race wrote receipt: %v", err)
	}
}

func TestPinnedKagentVersionAcceptsOfficialShortCommitOnly(t *testing.T) {
	if !validPinnedKagentVersion("v0.10.2", kagentPinnedCommit) {
		t.Fatal("exact Kagent version and commit refused")
	}
	if !validPinnedKagentVersion("0.10.2", kagentPinnedCommit[:7]) {
		t.Fatal("official release-style short Kagent commit refused")
	}
	for _, commit := range []string{kagentPinnedCommit[:6], "68df64g", kagentPinnedCommit + "0", "1234567"} {
		if validPinnedKagentVersion("v0.10.2", commit) {
			t.Fatalf("invalid Kagent commit %q accepted", commit)
		}
	}
}

func TestValidateKagentCRDUsesCanonicalJSON(t *testing.T) {
	want := kagentCRDIdentity{Name: "agents.kagent.dev", Kind: "Agent", Plural: "agents"}
	want.SchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(`{"properties":{"a":{"type":"string"},"z":{"type":"boolean"}},"type":"object"}`)))
	raw := []byte(`{"metadata":{"name":"agents.kagent.dev"},"spec":{"group":"kagent.dev","scope":"Namespaced","names":{"kind":"Agent","plural":"agents"},"versions":[{"name":"v1alpha2","served":true,"storage":true,"schema":{"openAPIV3Schema":{"type":"object","properties":{"z":{"type":"boolean"},"a":{"type":"string"}}}}}]}}`)
	if err := validateKagentCRD(raw, want); err != nil {
		t.Fatalf("canonical CRD schema refused: %v", err)
	}
	want.SchemaSHA256 = strings.Repeat("0", 64)
	if err := validateKagentCRD(raw, want); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("wrong CRD schema digest accepted: %v", err)
	}
}

func TestKagentTrustedProxyTaskRefusedBeforeWrites(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "trusted-proxy")
	opt.Task = "private prompt"
	err := a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "trusted-proxy") || strings.Contains(err.Error(), opt.Task) {
		t.Fatalf("trusted-proxy error = %v", err)
	}
	for _, call := range kagentCalls(t, dir) {
		if call.Body != nil {
			t.Fatalf("trusted-proxy refusal reached admission/write: %+v", call)
		}
	}
}

func TestKagentGeneratedWorkloadFailuresWriteNoReceipt(t *testing.T) {
	for _, scenario := range []string{"wrong-agent-imageid", "workload-replaced"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, _, dir := kagentCreateFixture(t, scenario)
			opt.Task = "private prompt"
			err := a.CreateAgent(opt)
			if err == nil || strings.Contains(err.Error(), opt.Task) || strings.Contains(out.String(), opt.Task) {
				t.Fatalf("error = %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(statErr) {
				t.Fatalf("failed workload proof wrote receipt: %v", statErr)
			}
			sends := 0
			for _, call := range kagentCalls(t, dir) {
				if call.Body["method"] == "message/send" {
					sends++
				}
			}
			if scenario == "wrong-agent-imageid" && sends != 0 || scenario == "workload-replaced" && sends != 1 {
				t.Fatalf("%s sends = %d", scenario, sends)
			}
		})
	}
}

func TestKagentFailureBeforeSuccessWritesNoReceipt(t *testing.T) {
	a, opt, _, _, _ := kagentCreateFixture(t, "")
	if err := os.MkdirAll(filepath.Join(opt.BundlePath, "receipts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.BundlePath, "receipts", "older.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed the portable bundle so create's normal persistence preflight accepts
	// the pre-existing real receipts directory.
	tools, err := parseKagentCreateTools(opt.Tools)
	if err != nil {
		t.Fatal(err)
	}
	source, bindings, err := portableKagentSource(opt, tools)
	if err != nil {
		t.Fatal(err)
	}
	bindingSource, err := agentruntime.EncodeKagentBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.BundlePath, "agent.yaml"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opt.BundlePath, "bindings.yaml"), bindingSource, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "wrong-version")
	if err := a.CreateAgent(opt); err == nil {
		t.Fatal("failed Kagent create unexpectedly succeeded")
	}
	entries, err := os.ReadDir(filepath.Join(opt.BundlePath, "receipts"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "older.json" {
		t.Fatalf("failed create changed receipts: %v, %v", entries, err)
	}
}

func TestKagentAgentCardRegistrationLagIsPolledWithoutMessageRetry(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "card-lag")
	opt.Task = "private prompt"
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	cards, sends := 0, 0
	for _, call := range kagentCalls(t, dir) {
		if slices.Contains(call.Args, "--raw") && slices.ContainsFunc(call.Args, func(arg string) bool { return strings.HasSuffix(arg, "/.well-known/agent-card.json") }) {
			cards++
		}
		if call.Body["method"] == "message/send" {
			sends++
		}
	}
	if cards < 3 || sends != 1 {
		t.Fatalf("agent card reads = %d, sends = %d", cards, sends)
	}
}

func TestKagentStaleAgentReadinessStopsWithoutTask(t *testing.T) {
	a, opt, out, _, dir := kagentCreateFixture(t, "stale-agent")
	opt.Task = "do not print this prompt"
	kagentReadinessTimeout = 50 * time.Millisecond
	err := a.CreateAgent(opt)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("stale Agent readiness error = %v", err)
	}
	if strings.Contains(err.Error(), opt.Task) || strings.Contains(out.String(), opt.Task) {
		t.Fatal("task prompt leaked")
	}
	sends := 0
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends++
		}
	}
	if sends != 0 {
		t.Fatalf("stale Agent sent %d tasks", sends)
	}
}

func TestKagentDryRunPerformsPreflightsButWritesNothing(t *testing.T) {
	a, opt, _, diagnostics, dir := kagentCreateFixture(t, "")
	opt.DryRun = true
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	dryRuns, writes, sends := 0, 0, 0
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends++
		}
		if call.Body["kind"] == nil {
			continue
		}
		if slices.Contains(call.Args, "--dry-run=server") {
			dryRuns++
		} else {
			writes++
		}
	}
	if dryRuns != 2 || writes != 0 || sends != 0 {
		t.Fatalf("dry-run calls: admission=%d writes=%d sends=%d", dryRuns, writes, sends)
	}
	if _, err := os.Stat(opt.Out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "not applied") {
		t.Fatal(diagnostics.String())
	}
	if _, err := os.Stat(filepath.Join(opt.BundlePath, "receipts")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote receipt: %v", err)
	}
}

func TestKagentCreateOnlyCollisionStopsBeforeAdmission(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "")
	seed := map[string]any{"apiVersion": scaffold.KagentAPIVersion, "kind": "ModelConfig", "metadata": map[string]any{
		"name": opt.Name, "namespace": opt.Namespace, "uid": "existing", "generation": 1,
	}}
	body, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, opt.Name+"-modelconfigs.kagent.dev.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	// Collision checks ask for -o name. Make that query truthful for this
	// seeded fixture by switching the helper scenario.
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "model-collision")
	err = a.CreateAgent(opt)
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "create-only") {
		t.Fatalf("collision error = %v", err)
	}
	for _, call := range kagentCalls(t, dir) {
		if call.Body != nil {
			t.Fatalf("collision reached admission/write: %+v", call)
		}
	}
}

func TestKagentGeneratedChildAndModelSecretNameCollisionsStopBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		scenario, kind string
		modelSecret    bool
	}{
		{"child-secret-collision", "Secret", false},
		{"child-serviceaccount-collision", "ServiceAccount", false},
		{"child-deployment-collision", "Deployment", false},
		{"child-service-collision", "Service", false},
		{"model-secret-same-name", "Secret", true},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, tc.scenario)
			if tc.modelSecret {
				opt.Secret = opt.Name
			}
			err := a.CreateAgent(opt)
			want := tc.kind + "/sample already exists"
			if tc.modelSecret {
				want = "model Secret name must differ"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("collision error = %v", err)
			}
			assertKagentNoClusterWrites(t, dir)
			assertKagentNoTaskOrReceipt(t, opt, dir)
		})
	}
}

func TestKagentInstallationScopeAndRBACFailBeforeWrites(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"excluded-namespace", "does not watch target namespace"},
		{"rbac-denied", "not allowed to create deployments.apps"},
		{"rbac-agent-list-denied", "not allowed to list agents.kagent.dev"},
		{"rbac-model-watch-denied", "not allowed to watch modelconfigs.kagent.dev"},
		{"rbac-model-status-denied", "not allowed to update modelconfigs.kagent.dev/status"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, tc.scenario)
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("scope proof error = %v", err)
			}
			assertKagentNoClusterWrites(t, dir)
			assertKagentNoTaskOrReceipt(t, opt, dir)
		})
	}
}

func TestKagentAdmissionCreateAndLiveSpecMutationRefuseTaskAndReceipt(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"dry-run-mutated", "server-mutated spec"},
		{"create-response-mutated", "mutated admitted object"},
		{"later-live-mutated", "admitted spec"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, tc.scenario)
			opt.Task = "private prompt"
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mutation error = %v", err)
			}
			assertKagentNoTaskOrReceipt(t, opt, dir)
		})
	}
}

func TestKagentRemoteMCPServerSnapshotDriftStopsBeforeAgentTaskAndReceipt(t *testing.T) {
	for _, scenario := range []string{"tool-replaced", "tool-spec-drift", "tool-secret-drift", "tool-set-drift"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, scenario)
			opt.Tools = "cluster-tools:read,list"
			opt.Task = "private prompt"
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), "RemoteMCPServer cluster-tools was replaced") {
				t.Fatalf("dependency drift error = %v", err)
			}
			for _, call := range kagentCalls(t, dir) {
				if call.Body["kind"] == "Agent" && !slices.Contains(call.Args, "--dry-run=server") {
					t.Fatalf("dependency drift reached Agent create: %+v", call)
				}
			}
			assertKagentNoTaskOrReceipt(t, opt, dir)
		})
	}
}

func TestKagentFinalReceiptAlwaysReverifiesPlatformAndCluster(t *testing.T) {
	for _, scenario := range []string{"final-platform-drift", "final-cluster-drift"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := kagentCreateFixture(t, scenario)
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), "deployment receipt") {
				t.Fatalf("final identity error = %v", err)
			}
			assertKagentNoTaskOrReceipt(t, opt, dir)
		})
	}
}

func TestKagentBundleRefusesEditsPartialDirectoriesAndOverlap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, opt *CreateOptions)
		want  string
	}{
		{"edited", func(t *testing.T, opt *CreateOptions) {
			if err := os.MkdirAll(opt.BundlePath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(opt.BundlePath, "agent.yaml"), []byte("edited"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(opt.BundlePath, "bindings.yaml"), []byte("edited"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "differs"},
		{"partial", func(t *testing.T, opt *CreateOptions) {
			if err := os.MkdirAll(opt.BundlePath, 0o755); err != nil {
				t.Fatal(err)
			}
			tools, err := parseKagentCreateTools(opt.Tools)
			if err != nil {
				t.Fatal(err)
			}
			source, _, err := portableKagentSource(*opt, tools)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(opt.BundlePath, "agent.yaml"), source, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "bindings.yaml"},
		{"overlap", func(_ *testing.T, opt *CreateOptions) {
			opt.Out = filepath.Join(opt.BundlePath, "rendered.yaml")
		}, "inside the bundle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			root := t.TempDir()
			opt := kagentOfflineOptions(filepath.Join(root, "sample.yaml"), filepath.Join(root, "bundle"))
			tc.setup(t, &opt)
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe bundle path was accepted: %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("unsafe bundle emitted stdout")
			}
		})
	}
}

func TestKagentBundlePreflightAllowsOnlyRealReceiptsDirectory(t *testing.T) {
	root := t.TempDir()
	opt := kagentOfflineOptions(filepath.Join(root, "sample.yaml"), filepath.Join(root, "bundle"))
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(opt.BundlePath, "receipts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(opt.Out); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAgent(opt); err != nil {
		t.Fatalf("real receipts directory refused: %v", err)
	}
	if err := os.Remove(filepath.Join(opt.BundlePath, "receipts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(opt.BundlePath, "receipts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(opt.Out); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAgent(opt); err == nil || !strings.Contains(err.Error(), "receipts") {
		t.Fatalf("receipts symlink accepted: %v", err)
	}
}

func TestKagentTaskUsesNonemptyIDsAndSendsOnce(t *testing.T) {
	a, opt, out, diagnostics, dir := kagentCreateFixture(t, "")
	opt.Task = "private task prompt"
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	var sends []kagentCall
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends = append(sends, call)
			if !slices.Contains(call.Args, "create") || !slices.Contains(call.Args, "--raw") || !slices.Contains(call.Args, "-f") || !slices.Contains(call.Args, "-") {
				t.Fatalf("message/send did not use kubectl create --raw ... -f -: %v", call.Args)
			}
		}
	}
	if len(sends) != 1 {
		t.Fatalf("message/send calls = %d", len(sends))
	}
	request := sends[0].Body
	if strings.TrimSpace(fmt.Sprint(request["id"])) == "" {
		t.Fatal("JSON-RPC id is empty")
	}
	params := request["params"].(map[string]any)
	message := params["message"].(map[string]any)
	if strings.TrimSpace(fmt.Sprint(message["messageId"])) == "" {
		t.Fatal("A2A messageId is empty")
	}
	if out.String() != "one answer\n" {
		t.Fatalf("answer output = %q", out.String())
	}
	if strings.Contains(diagnostics.String(), opt.Task) {
		t.Fatal("task prompt leaked into diagnostics")
	}
	artifact, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifact), opt.Task) || strings.Contains(string(artifact), "one answer") {
		t.Fatal("task prompt or answer was persisted in artifact")
	}
	for _, text := range []string{"stores the full task prompt, history, and answer", "session retention is unlimited", "audit policy may capture Service-proxy request and response bodies"} {
		if !strings.Contains(diagnostics.String(), text) {
			t.Fatalf("task guard disclosure lacks %q: %s", text, diagnostics.String())
		}
	}
}

func TestCompletedKagentAnswerStrictSafety(t *testing.T) {
	rpcID := "rpc-1"
	for _, tc := range []struct {
		name, response, want, wantErr string
	}{
		{
			name:     "data artifact falls back to latest agent history",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed"},"artifacts":[{"parts":[{"kind":"data","data":{"answer":"not text"}}]}],"history":[{"role":"agent","messageId":"message-1","contextId":"ctx-1","parts":[{"kind":"text","text":"history answer"}]}]}}`,
			want:     "history answer",
		},
		{
			name:     "text artifact without message identity is refused",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed"},"artifacts":[{"parts":[{"kind":"text","text":"unbound answer"}]}]}}`,
			wantErr:  "no nonblank agent text answer",
		},
		{
			name:     "valid live shaped status response",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed","message":{"kind":"message","role":"agent","messageId":"message-1","contextId":"ctx-1","parts":[{"kind":"text","text":"safe\u001b[2J\u0000 answer"}]}}}}`,
			want:     "safe answer",
		},
		{
			name:     "assistant role direct message",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"message","messageId":"message-1","role":"assistant","parts":[{"kind":"text","text":"answer"}]}}`,
			wantErr:  "exact agent identity",
		},
		{
			name:     "user role direct message",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"message","messageId":"message-1","role":"user","parts":[{"kind":"text","text":"prompt echo"}]}}`,
			wantErr:  "identity",
		},
		{
			name:     "user role task status and history",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed","message":{"role":"user","messageId":"message-1","contextId":"ctx-1","parts":[{"kind":"text","text":"prompt echo"}]}},"history":[{"role":"user","messageId":"message-2","contextId":"ctx-1","parts":[{"kind":"text","text":"prompt echo"}]}]}}`,
			wantErr:  "matching agent message identity",
		},
		{
			name:     "wrong string id",
			response: `{"jsonrpc":"2.0","id":"other","result":{"kind":"message","messageId":"message-1","role":"agent","parts":[{"kind":"text","text":"answer"}]}}`,
			wantErr:  "different RPC id",
		},
		{
			name:     "wrong id type",
			response: `{"jsonrpc":"2.0","id":1,"result":{"kind":"message","messageId":"message-1","role":"agent","parts":[{"kind":"text","text":"answer"}]}}`,
			wantErr:  "different RPC id",
		},
		{
			name:     "missing task identity",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","status":{"state":"completed"},"artifacts":[{"parts":[{"kind":"text","text":"answer"}]}]}}`,
			wantErr:  "task or context identity",
		},
		{
			name:     "blank direct message identity",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"message","messageId":"  ","role":"agent","parts":[{"kind":"text","text":"answer"}]}}`,
			wantErr:  "identity",
		},
		{
			name:     "untrusted state not echoed",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"bad\u001b[2Jsecret"}}}`,
			wantErr:  "required completed state",
		},
		{
			name:     "blank part kind is not text",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"message","messageId":"message-1","role":"agent","parts":[{"text":"answer"}]}}`,
			wantErr:  "explicitly declare kind text",
		},
		{
			name:     "missing nested message id",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed","message":{"role":"agent","contextId":"ctx-1","parts":[{"kind":"text","text":"answer"}]}}}}`,
			wantErr:  "matching agent message identity",
		},
		{
			name:     "mismatched nested context id",
			response: `{"jsonrpc":"2.0","id":"rpc-1","result":{"kind":"task","id":"task-1","contextId":"ctx-1","status":{"state":"completed"},"history":[{"role":"agent","messageId":"message-1","contextId":"other","parts":[{"kind":"text","text":"answer"}]}]}}`,
			wantErr:  "matching agent message identity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := completedKagentAnswer([]byte(tc.response), rpcID)
			if tc.wantErr == "" {
				if err != nil || answer != tc.want {
					t.Fatalf("answer = %q, error = %v", answer, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("answer = %q, error = %v", answer, err)
			}
		})
	}
}

func TestSendKagentTaskDoesNotRetryWrongResponse(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kubectl", "exec "+shellArg(exe)+" -test.run=^TestKagentKubectlHelper$ -- \"$@\"")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("KMX_KAGENT_TEST_DIR", dir)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", "wrong-rpc-id")
	runner := &run.Runner{Stdout: io.Discard, Stderr: io.Discard}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: runner, Out: io.Discard, Err: io.Discard}
	_, err = a.sendKagentTask(t.Context(), kagentControllerService{Name: "kagent-controller", Namespace: "kagent"}, "agents", "sample", "private prompt")
	if err == nil || !strings.Contains(err.Error(), "different RPC id") {
		t.Fatalf("wrong RPC id error = %v", err)
	}
	sends := 0
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("message sends = %d, want 1", sends)
	}
}

func TestKagentTaskPromptNeverReachesArgvOrErrors(t *testing.T) {
	a, opt, _, diagnostics, dir := kagentCreateFixture(t, "wrong-version")
	opt.Task = "prompt-private-" + strings.Repeat("x", 20)
	err := a.CreateAgent(opt)
	if err == nil {
		t.Fatal("wrong Kagent version unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), opt.Task) || strings.Contains(diagnostics.String(), opt.Task) {
		t.Fatal("task prompt leaked into an error or diagnostic")
	}
	for _, call := range kagentCalls(t, dir) {
		if strings.Contains(strings.Join(call.Args, " "), opt.Task) {
			t.Fatal("task prompt reached kubectl argv")
		}
	}
}

func TestKagentLifecycleUnsupportedVerbsAreTyped(t *testing.T) {
	adapter := kagentRuntimeAdapter{}
	if capabilities := adapter.Capabilities(); capabilities.Render || capabilities.Deploy || capabilities.Status || capabilities.Evaluate {
		t.Fatalf("unconfigured Kagent adapter declares capabilities: %+v", capabilities)
	}
	for verb, call := range map[string]func() error{
		agentruntime.VerbStatus: func() error {
			_, err := adapter.Status(t.Context(), agentruntime.AgentRef{}, agentruntime.StatusOptions{})
			return err
		},
		agentruntime.VerbEvaluate: func() error {
			_, err := adapter.Evaluate(t.Context(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
			return err
		},
	} {
		err := call()
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) || unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != verb {
			t.Fatalf("%s error = %#v", verb, err)
		}
	}
}

func TestKagentDeployRefusesMutatedRenderedDocuments(t *testing.T) {
	opt := kagentOfflineOptions("-", "")
	tools, err := parseKagentCreateTools("")
	if err != nil {
		t.Fatal(err)
	}
	source, bindings, err := portableKagentSource(opt, tools)
	if err != nil {
		t.Fatal(err)
	}
	adapter := kagentRuntimeAdapter{app: &App{}, create: &opt, bindings: &bindings}
	rendered, err := adapter.Render(t.Context(), source, agentruntime.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	documents := rendered.Documents()
	documents[2] = append(documents[2], []byte("kind: Agent\n")...)
	mutated, err := agentruntime.NewRenderedBundle(agentruntime.Kagent, source, []agentruntime.Document{
		agentruntime.ReviewDocument(documents[0]),
		agentruntime.ApplyDocument(documents[1]),
		agentruntime.ApplyDocument(documents[2]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kagentBundleFromRendered(mutated); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("mutated rendered bytes were accepted: %v", err)
	}
}

func TestKagentToolsBindingRendersAndPreflights(t *testing.T) {
	a, opt, _, _, dir := kagentCreateFixture(t, "")
	opt.Tools = "cluster-tools:read,list"
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	foundRead := false
	for _, call := range kagentCalls(t, dir) {
		if slices.Contains(call.Args, "remotemcpservers.kagent.dev") {
			foundRead = true
		}
	}
	if !foundRead {
		t.Fatal("configured tools did not preflight RemoteMCPServer")
	}
	body, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc["kind"] == "Agent" {
			declarative := doc["spec"].(map[string]any)["declarative"].(map[string]any)
			if _, ok := declarative["tools"].([]any); !ok {
				t.Fatal("Kagent tools were not rendered")
			}
		}
	}
}
