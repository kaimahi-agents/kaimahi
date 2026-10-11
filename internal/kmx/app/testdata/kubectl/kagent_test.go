package kubectl

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
)

// TestKagentKubectlFixturePinsMatchProduction in the App package verifies these
// copied fixture pins without pulling App's dependency graph into this binary.
const (
	kagentPinnedCommit = "68df64f671800c37c4204d81ebe0dd66ec35d223"

	kagentControllerImage     = "ghcr.io/kagent-dev/kagent/controller:0.10.2"
	kagentControllerImageRepo = "ghcr.io/kagent-dev/kagent/controller"
	kagentGoImage             = "ghcr.io/kagent-dev/kagent/golang-adk:0.10.2"
	kagentGoImageRepo         = "ghcr.io/kagent-dev/kagent/golang-adk"
	kagentPythonImage         = "ghcr.io/kagent-dev/kagent/app:0.10.2"
	kagentPythonImageRepo     = "ghcr.io/kagent-dev/kagent/app"

	kagentControllerAMD64Digest = "01236c6253abb2f35e25fa4af463c06780052065b7161eb29f3b162703e29075"
	kagentGoAMD64Digest         = "922dd44552c55cad46c9e38c9d56b1138a682114e665e89b60caebde8f1952f6"
	kagentPythonAMD64Digest     = "7dfa664fde480f5504098e5937492312c218091996857c32c5269c67e5a1048f"
)

type kagentCall struct {
	Args []string       `json:"args"`
	Body map[string]any `json:"body,omitempty"`
}

func TestKagentKubectlPinsHelper(t *testing.T) {
	if os.Getenv("KMX_KAGENT_TEST_DIR") == "" {
		return
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"commit":                  kagentPinnedCommit,
		"controller_image":        kagentControllerImage,
		"controller_repository":   kagentControllerImageRepo,
		"controller_amd64_digest": kagentControllerAMD64Digest,
		"go_image":                kagentGoImage,
		"go_repository":           kagentGoImageRepo,
		"go_amd64_digest":         kagentGoAMD64Digest,
		"python_image":            kagentPythonImage,
		"python_repository":       kagentPythonImageRepo,
		"python_amd64_digest":     kagentPythonAMD64Digest,
	}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
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
			time.Sleep(2 * time.Second)
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
				time.Sleep(time.Second)
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
			if scenario == "slow-admission" {
				time.Sleep(time.Second)
				fail()
			}
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

func kagentPlural(kind string) string {
	switch kind {
	case "Secret":
		return "secrets"
	case "ServiceAccount":
		return "serviceaccounts"
	case "Deployment":
		return "deployments.apps"
	case "Service":
		return "services"
	case "ModelConfig":
		return "modelconfigs.kagent.dev"
	case "Agent":
		return "agents.kagent.dev"
	default:
		return strings.ToLower(kind) + "s.kagent.dev"
	}
}
