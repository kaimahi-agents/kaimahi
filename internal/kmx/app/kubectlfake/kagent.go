package kubectlfake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// TestKagentKubectlFixturePinsMatchProduction in App verifies these fixture pins.
const (
	kagentPinnedCommit          = "68df64f671800c37c4204d81ebe0dd66ec35d223"
	kagentControllerImage       = "ghcr.io/kagent-dev/kagent/controller:0.10.2"
	kagentControllerImageRepo   = "ghcr.io/kagent-dev/kagent/controller"
	kagentGoImage               = "ghcr.io/kagent-dev/kagent/golang-adk:0.10.2"
	kagentGoImageRepo           = "ghcr.io/kagent-dev/kagent/golang-adk"
	kagentPythonImage           = "ghcr.io/kagent-dev/kagent/app:0.10.2"
	kagentPythonImageRepo       = "ghcr.io/kagent-dev/kagent/app"
	kagentControllerAMD64Digest = "01236c6253abb2f35e25fa4af463c06780052065b7161eb29f3b162703e29075"
	kagentGoAMD64Digest         = "922dd44552c55cad46c9e38c9d56b1138a682114e665e89b60caebde8f1952f6"
	kagentPythonAMD64Digest     = "7dfa664fde480f5504098e5937492312c218091996857c32c5269c67e5a1048f"
)

type kagentCall struct {
	Args []string       `json:"args"`
	Body map[string]any `json:"body,omitempty"`
}

func (r Request) kagentPins() {
	if r.Getenv("KMX_KAGENT_TEST_DIR") == "" {
		return
	}
	if err := json.NewEncoder(r.Stdout).Encode(map[string]string{
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
		exit(2)
	}
	exit(0)
}

// The same stateful handler serves executable and in-process requests.
func (r Request) kagent() {
	dir := r.Getenv("KMX_KAGENT_TEST_DIR")
	if dir == "" {
		return
	}
	args := r.Args
	if slices.Equal(args, []string{"version", "--client"}) {
		exit(0)
	}
	stdin, _ := io.ReadAll(r.Stdin)
	call := kagentCall{Args: args}
	if len(bytes.TrimSpace(stdin)) > 0 {
		_ = json.Unmarshal(stdin, &call.Body)
	}
	log, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		exit(2)
	}
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	fail := func() { fmt.Fprint(r.Stderr, "external Kagent test failure"); exit(1) }
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	scenario := r.Getenv("KMX_KAGENT_TEST_SCENARIO")
	fixtureTag := "0.10.2"
	commit := kagentPinnedCommit
	controllerImage, goImage := kagentControllerImage, kagentGoImage
	controllerDigest, goDigest := kagentControllerAMD64Digest, kagentGoAMD64Digest
	if r.Getenv("KMX_KAGENT_TEST_VERSION") == "v0.10.3" {
		fixtureTag = "0.10.3"
		commit = "8878c39ac64421cb665c8e62ecc6411dd12f2fc2"
		controllerImage = "ghcr.io/kagent-dev/kagent/controller:0.10.3"
		goImage = "ghcr.io/kagent-dev/kagent/golang-adk:0.10.3"
		controllerDigest = "8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8"
		goDigest = "7f788d09d5f21ccc3068679310943dcf4f0df19e1a93960423810438f4238b24"
	}
	if scenario == "digest-images" {
		controllerImage += "@sha256:b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"
		goImage = "ghcr.io/kagent-dev/kagent/golang-adk@sha256:5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f"
	}
	workloadTemplateExtra, workloadContainerExtra := "", ""
	switch scenario {
	case "workload-security-mutated":
		workloadTemplateExtra = `,"securityContext":{"runAsUser":0}`
	case "rs-template-adjacent-int64":
		workloadTemplateExtra = `,"securityContext":{"runAsUser":9007199254740992}`
	case "workload-template-mutated":
		workloadTemplateExtra = `,"automountServiceAccountToken":true`
	case "workload-sidecar-mutated":
		workloadContainerExtra = `,{"name":"foreign","image":"example.invalid/foreign:latest"}`
	}
	podOwners := func(controller bool) string {
		name, uid := "sample-rs", "agent-rs-uid"
		if controller {
			name, uid = "controller-rs", "controller-rs-uid"
		}
		if !controller {
			switch scenario {
			case "pod-owner-missing":
				return ""
			case "pod-owner-foreign":
				uid = "foreign-rs-uid"
			case "pod-owner-old":
				name, uid = "sample-old-rs", "old-rs-uid"
			case "pod-owner-duplicate":
				return `,"ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"sample-rs","uid":"agent-rs-uid","controller":true},{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"sample-rs","uid":"agent-rs-uid","controller":true}]`
			}
		} else if scenario == "controller-pod-owner-foreign" {
			uid = "foreign-rs-uid"
		}
		return fmt.Sprintf(`,"ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":%q,"uid":%q,"controller":true}]`, name, uid)
	}
	printJSON := func(format string, args ...any) {
		fmt.Fprint(r.Stdout, strings.ReplaceAll(fmt.Sprintf(format, args...), "0.10.2", fixtureTag))
	}
	projection := func() string {
		i := slices.Index(args, "-o")
		if i >= 0 {
			return args[i+1]
		}
		return ""
	}
	agentExists := func() bool { _, err := os.Stat(filepath.Join(dir, "sample-agents.kagent.dev.json")); return err == nil }
	invalidResponse := func(prefix string) {
		if scenario == prefix+"-unreadable" {
			fail()
		}
		if scenario == prefix+"-malformed" {
			fmt.Fprint(r.Stdout, "{")
			exit(0)
		}
	}
	if slices.Contains(args, "config") {
		if scenario == "remote-context" {
			fmt.Fprint(r.Stdout, `{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":"https://remote.example:6443"}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`)
			exit(0)
		}
		fmt.Fprint(r.Stdout, `{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`)
		exit(0)
	}
	if rawIndex := slices.Index(args, "--raw"); rawIndex >= 0 {
		path := args[rawIndex+1]
		if strings.HasSuffix(path, "/version") {
			version := "v" + fixtureTag
			if scenario == "wrong-version" {
				version = "v0.10.1"
				commit = "1234567"
			}
			fmt.Fprintf(r.Stdout, `{"kagent_version":%q,"git_commit":%q}`, version, commit)
			exit(0)
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
			fmt.Fprint(r.Stdout, `{"name":"sample","capabilities":{"streaming":true},"defaultInputModes":["text"],"defaultOutputModes":["text"]}`)
			exit(0)
		}
		if call.Body["method"] != "message/send" || !strings.Contains(path, "/api/a2a/agents/sample") {
			fail()
		}
		if scenario == "task-timeout" {
			_ = os.WriteFile(filepath.Join(dir, "message-attempted"), []byte("attempted"), 0o600)
			r.delay(2 * time.Second)
			fail()
		}
		id, _ := call.Body["id"].(string)
		_ = os.WriteFile(filepath.Join(dir, "message-sent"), []byte("sent"), 0o600)
		if scenario == "wrong-rpc-id" {
			id = "other"
		}
		fmt.Fprintf(r.Stdout, `{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"task-1","contextId":"context-1","status":{"state":"completed","message":{"kind":"message","role":"agent","messageId":"answer-1","contextId":"context-1","parts":[{"kind":"text","text":"one answer"}]}}}}`, id)
		exit(0)
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
			fmt.Fprint(r.Stdout, "no\n")
		} else {
			fmt.Fprint(r.Stdout, "yes\n")
		}
		exit(0)
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
				if scenario == "child-service-collision" || fixtureTag == "0.10.3" && agentExists() {
					fmt.Fprint(r.Stdout, "present")
				}
				exit(0)
			}
			if scenario == "slow-preflight" {
				r.delay(time.Second)
				fail()
			}
			count := markInstallRead()
			imageTag := fixtureTag
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
			printJSON(`{"items":[{"metadata":{"name":"kagent-controller-metrics","namespace":"kagent","uid":"metrics-service-uid","resourceVersion":"4","labels":{"app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"selector":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"},"ports":[{"name":"https","protocol":"TCP","port":8443}]}},{"metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-service-uid","resourceVersion":%q,"labels":{"app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller","app.kubernetes.io/version":%q,"helm.sh/chart":"kagent-0.10.2"}},"spec":{"selector":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"},"ports":[{"name":"controller","protocol":"TCP","port":8083}]}}]}`, resourceVersion, imageTag)
		case "deployments":
			image := controllerImage
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
			printJSON(`{"items":[{"kind":"Deployment","metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-deployment-uid","resourceVersion":"6","generation":1,"annotations":{"deployment.kubernetes.io/revision":"1"},"labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/part-of":"kagent","app.kubernetes.io/component":"controller","app.kubernetes.io/version":"0.10.2","helm.sh/chart":"kagent-0.10.2"}},"spec":{"replicas":1,"selector":{"matchLabels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"template":{"metadata":{"labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"serviceAccountName":"kagent-controller","containers":[{"name":"controller","image":%q%s,"env":[{"name":"AUTH_MODE","value":%q}],"envFrom":[{"configMapRef":{"name":"kagent-controller"}}]}]}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}`, image, commandOverride, authMode)
		case "deployments.apps":
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-deployment-collision" || fixtureTag == "0.10.3" && agentExists() {
				fmt.Fprint(r.Stdout, "present")
			}
		case "replicasets.apps":
			invalidResponse("rs")
			controller := strings.Contains(args[slices.Index(args, "-l")+1], "app.kubernetes.io/component=controller")
			name, uid, deploymentName, deploymentUID := "sample-rs", "agent-rs-uid", "sample", "agent-deployment-uid"
			labels, templateSpec := `{"app":"kagent","kagent":"sample"}`, fmt.Sprintf(`{"containers":[{"name":"kagent","image":%q}%s]%s}`, goImage, workloadContainerExtra, workloadTemplateExtra)
			revision := "1"
			if controller {
				name, uid, deploymentName, deploymentUID = "controller-rs", "controller-rs-uid", "kagent-controller", "controller-deployment-uid"
				labels = `{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}`
				templateSpec = fmt.Sprintf(`{"serviceAccountName":"kagent-controller","containers":[{"name":"controller","image":%q,"env":[{"name":"AUTH_MODE","value":"unsecure"}],"envFrom":[{"configMapRef":{"name":"kagent-controller"}}]}]}`, controllerImage)
			} else {
				if scenario == "rs-owner-foreign" {
					deploymentUID = "foreign-deployment-uid"
				}
				if scenario == "rs-revision-old" {
					revision = "0"
				}
				if scenario == "rs-identity-missing" {
					uid = ""
				}
				if scenario == "rs-template-mutated" {
					templateSpec = fmt.Sprintf(`{"containers":[{"name":"kagent","image":%q}],"hostNetwork":true}`, goImage)
				}
				if scenario == "rs-template-adjacent-int64" {
					templateSpec = strings.Replace(templateSpec, "9007199254740992", "9007199254740993", 1)
				}
			}
			fmt.Fprintf(r.Stdout, `{"apiVersion":"apps/v1","kind":"ReplicaSetList","items":[{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"name":%q,"namespace":%q,"uid":%q,"resourceVersion":"30","generation":1,"labels":%s,"annotations":{"deployment.kubernetes.io/revision":%q},"ownerReferences":[{"apiVersion":"apps/v1","kind":"Deployment","name":%q,"uid":%q,"controller":true}]},"spec":{"replicas":1,"selector":{"matchLabels":%s},"template":{"metadata":{"labels":%s},"spec":%s}},"status":{"observedGeneration":1,"replicas":1,"readyReplicas":1,"availableReplicas":1}}]}`, name, map[bool]string{true: "kagent", false: "agents"}[controller], uid, labels, revision, deploymentName, deploymentUID, labels, labels, templateSpec)
		case "deployment":
			invalidResponse("workload")
			name := args[getIndex+2]
			if name != "sample" {
				fail()
			}
			image := goImage
			if r.Getenv("KMX_KAGENT_TEST_RUNTIME") == "python" {
				image = kagentPythonImage
			}
			uid, resourceVersion := "agent-deployment-uid", "20"
			if scenario == "workload-replaced" {
				if _, err := os.Stat(filepath.Join(dir, "message-sent")); err == nil {
					uid, resourceVersion = "replacement-deployment-uid", "21"
				}
			}
			if scenario == "workload-identity-incomplete" {
				uid = ""
			}
			owner := "agent-uid"
			if scenario == "workload-owner-foreign" {
				owner = "foreign-agent-uid"
			}
			ready := 1
			if scenario == "workload-notready" {
				ready = 0
			}
			fmt.Fprintf(r.Stdout, `{"kind":"Deployment","metadata":{"name":"sample","namespace":"agents","uid":%q,"resourceVersion":%q,"generation":1,"annotations":{"deployment.kubernetes.io/revision":"1"},"labels":{"app":"kagent","kagent":"sample"},"ownerReferences":[{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","name":"sample","uid":%q,"controller":true}]},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"kagent","kagent":"sample"}},"template":{"metadata":{"labels":{"app":"kagent","kagent":"sample"}},"spec":{"containers":[{"name":"kagent","image":%q}%s]%s}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":%d,"availableReplicas":%d,"unavailableReplicas":0}}`, uid, resourceVersion, owner, image, workloadContainerExtra, workloadTemplateExtra, ready, ready)
		case "pods":
			selectorIndex := slices.Index(args, "-l")
			if selectorIndex < 0 {
				fail()
			}
			selector := args[selectorIndex+1]
			// Keep v0.10.3 controller qualification valid for workload status categories.
			if fixtureTag != "0.10.3" || !strings.Contains(selector, "app.kubernetes.io/component=controller") {
				if response, ok := map[string]string{"pods-missing-items": `{}`, "pods-null-list": `null`, "pods-null-items": `{"items":null}`, "pods-empty": `{"items":[]}`}[scenario]; ok {
					fmt.Fprint(r.Stdout, response)
					exit(0)
				}
			}
			invalidResponse("pods")
			if strings.Contains(selector, "app.kubernetes.io/component=controller") {
				imageID := "containerd://" + kagentControllerImageRepo + "@sha256:" + controllerDigest
				if scenario == "wrong-controller-imageid" {
					imageID = "containerd://" + kagentControllerImageRepo + "@sha256:bad"
				}
				statusImage := controllerImage
				podImage := controllerImage
				if strings.HasPrefix(scenario, "status-image-config") {
					statusImage = "sha256:84a2b0198392941e3739df6751d642026975a26e2fe290864b862e0d88d2d380"
					if fixtureTag == "0.10.3" {
						imageID = "sha256:b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"
					}
					if scenario == "status-image-config-arm64" || scenario == "status-image-config-wrong-platform" {
						statusImage = "sha256:151f6d0a41d3f1148e6cfe150f562b4e0501d0bde6fa2b3aa0b54105df8b5a0a"
					}
					if scenario == "status-image-config-unknown" {
						statusImage = "sha256:" + strings.Repeat("0", 64)
					}
					if scenario == "status-image-config-wrong-repository" {
						statusImage = "sha256:869b98f1e423328ff1eab3b65004e1bf5a4893fdb228044026423e32c9388c75"
					}
					if scenario == "status-image-config-wrong-imageid" {
						imageID = "sha256:" + strings.Repeat("0", 64)
					}
					if scenario == "status-image-config-wrong-pod-reference" {
						podImage = "ghcr.io/kagent-dev/kagent/controller:latest"
					}
				}
				fmt.Fprintf(r.Stdout, `{"items":[{"metadata":{"name":"kagent-controller-pod","namespace":"kagent","uid":"controller-pod-uid","resourceVersion":"7"%s,"labels":{"app.kubernetes.io/name":"kagent","app.kubernetes.io/instance":"kagent","app.kubernetes.io/component":"controller"}},"spec":{"nodeName":"node-1","containers":[{"name":"controller","image":%q}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"controller","image":%q,"imageID":%q,"ready":true}]}}]}`, podOwners(true), podImage, statusImage, imageID)
			} else {
				image, repository, digest := goImage, kagentGoImageRepo, goDigest
				if r.Getenv("KMX_KAGENT_TEST_RUNTIME") == "python" {
					image, repository, digest = kagentPythonImage, kagentPythonImageRepo, kagentPythonAMD64Digest
				}
				if scenario == "agent-platform-wrong-imageid" {
					digest = "0eb8becbaf323df2c29c8e73a14a775d24bf9c2f9314bd8a12a1da6e57e851fc"
				}
				if scenario == "wrong-agent-imageid" {
					digest = "bad"
				}
				statusImage := image
				if strings.HasPrefix(scenario, "status-image-config") {
					statusImage = "sha256:869b98f1e423328ff1eab3b65004e1bf5a4893fdb228044026423e32c9388c75"
					if scenario == "status-image-config-arm64" {
						statusImage = "sha256:eb435a67036b5cdc8fa20514a07c8d17964eeb2369dc741096d9129b2efafae3"
						digest = "0eb8becbaf323df2c29c8e73a14a775d24bf9c2f9314bd8a12a1da6e57e851fc"
					}
				}
				ready := "True"
				if scenario == "pod-notready" {
					ready = "False"
				}
				foreign := ""
				if scenario == "foreign-matching-pod" {
					foreign = fmt.Sprintf(`,{"metadata":{"name":"foreign-pod","namespace":"agents","uid":"foreign-pod-uid","resourceVersion":"99","labels":{"app":"kagent","kagent":"sample"}},"spec":{"nodeName":"node-1","containers":[{"name":"kagent","image":%q}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"kagent","image":%q,"imageID":%q,"ready":true}]}}`, image, image, "containerd://"+repository+"@sha256:"+digest)
				}
				fmt.Fprintf(r.Stdout, `{"items":[{"metadata":{"name":"sample-pod","namespace":"agents","uid":"agent-pod-uid","resourceVersion":"22"%s,"labels":{"app":"kagent","kagent":"sample"}},"spec":{"nodeName":"node-1","containers":[{"name":"kagent","image":%q}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":%q}],"containerStatuses":[{"name":"kagent","image":%q,"imageID":%q,"ready":true}]}}%s]}`, podOwners(false), image, ready, statusImage, "containerd://"+repository+"@sha256:"+digest, foreign)
			}
		case "configmap":
			tag := fixtureTag
			if scenario == "digest-images" {
				tag += "@sha256:5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f"
			}
			if scenario == "wrong-image-config" {
				tag = "latest"
			}
			watchNamespaces := ""
			if scenario == "excluded-namespace" {
				watchNamespaces = "other,more"
			}
			printJSON(`{"kind":"ConfigMap","metadata":{"name":"kagent-controller","namespace":"kagent","uid":"controller-config-uid","resourceVersion":"8","labels":{"app.kubernetes.io/version":"0.10.2","helm.sh/chart":"kagent-0.10.2"}},"data":{"IMAGE_REGISTRY":"ghcr.io","IMAGE_REPOSITORY":"kagent-dev/kagent/app","IMAGE_TAG":%q,"GO_IMAGE_REGISTRY":"ghcr.io","GO_IMAGE_REPOSITORY":"kagent-dev/kagent/golang-adk","GO_IMAGE_TAG":%q,"WATCH_NAMESPACES":%q}}`, tag, tag, watchNamespaces)
		case "crd":
			name := args[getIndex+2]
			kindName, plural := "Agent", "agents"
			if name == "modelconfigs.kagent.dev" {
				kindName, plural = "ModelConfig", "modelconfigs"
			} else if name == "remotemcpservers.kagent.dev" {
				kindName, plural = "RemoteMCPServer", "remotemcpservers"
			} else if name != "agents.kagent.dev" {
				fail()
			}
			schemaType := "object"
			if scenario == "wrong-schema" && name == "agents.kagent.dev" || scenario == "wrong-remote-schema" && name == "remotemcpservers.kagent.dev" {
				schemaType = "string"
			}
			fmt.Fprintf(r.Stdout, `{"metadata":{"name":%q},"spec":{"group":"kagent.dev","scope":"Namespaced","names":{"kind":%q,"plural":%q},"versions":[{"name":"v1alpha1","served":true,"storage":false},{"name":"v1alpha2","served":true,"storage":true,"schema":{"openAPIV3Schema":{"type":%q}}}]}}`, name, kindName, plural, schemaType)
		case "node":
			if agentExists() {
				invalidResponse("node")
			}
			arch := "amd64"
			if scenario == "status-image-config-arm64" {
				arch = "arm64"
			}
			if scenario == "unsupported-platform" {
				arch = "s390x"
			}
			printJSON(`{"kind":"Node","metadata":{"name":"node-1","uid":"node-uid","labels":{"kubernetes.io/os":"linux","kubernetes.io/arch":%q}},"status":{"nodeInfo":{"operatingSystem":"linux","architecture":%q}}}`, arch, arch)
		case "namespace":
			if args[getIndex+2] != "kube-system" {
				fail()
			}
			uid := "cluster-uid"
			if scenario == "selected-cluster-rebound" {
				marker := filepath.Join(dir, "selected-cluster-read")
				if _, err := os.Stat(marker); err == nil {
					uid = "replacement-cluster-uid"
				} else {
					_ = os.WriteFile(marker, []byte("read"), 0600)
				}
			}
			if scenario == "final-cluster-drift" {
				if _, err := os.Stat(filepath.Join(dir, "sample-agents.kagent.dev.json")); err == nil {
					uid = "replacement-cluster-uid"
				}
			}
			fmt.Fprintf(r.Stdout, `{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid)
		case "service":
			invalidResponse("service")
			_ = os.WriteFile(filepath.Join(dir, "workload-read"), []byte("read"), 0600)
			if args[getIndex+2] != "sample" {
				fail()
			}
			owner := "agent-uid"
			if scenario == "service-owner-foreign" {
				owner = "foreign-agent-uid"
			}
			fmt.Fprintf(r.Stdout, `{"kind":"Service","metadata":{"name":"sample","namespace":"agents","uid":"agent-service-uid","resourceVersion":"23","ownerReferences":[{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","name":"sample","uid":%q,"controller":true}]},"spec":{"selector":{"app":"kagent","kagent":"sample"},"ports":[{"name":"http","protocol":"TCP","port":8080}]}}`, owner)
		case "secret":
			name := args[getIndex+2]
			if name == "sample" {
				if scenario == "child-secret-collision" || scenario == "model-secret-same-name" {
					fmt.Fprint(r.Stdout, "present")
				}
				exit(0)
			}
			if fixtureTag == "0.10.3" && strings.HasPrefix(projection(), "go-template={{.kind}}") {
				rv := "key-rv"
				if scenario == "dependency-drift" && agentExists() {
					rv = "changed"
				}
				presence := "present"
				if scenario == "dependency-missing-key" {
					presence = ""
				}
				fmt.Fprintf(r.Stdout, "Secret\n%s\nagents\nkey-uid\n%s\nactive\n%s", name, rv, presence)
			} else {
				fmt.Fprint(r.Stdout, "secret\npresent")
			}
		case "secrets":
			if strings.HasPrefix(projection(), "go-template={{.kind}}") {
				invalidResponse("generated-secret")
			}
			if scenario == "child-unreadable" {
				fail()
			}
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-secret-collision" || scenario == "model-secret-same-name" {
				fmt.Fprint(r.Stdout, "present")
			} else if fixtureTag == "0.10.3" && agentExists() {
				if strings.HasPrefix(projection(), "go-template={{.kind}}") {
					owner := "agent-uid"
					if scenario == "generated-secret-owner-foreign" {
						owner = "foreign-agent-uid"
					}
					fmt.Fprintf(r.Stdout, "Secret\nsample\nagents\ngenerated-secret-uid\n25\nactive\nkagent.dev/v1alpha2|Agent|sample|%s\n", owner)
				} else {
					fmt.Fprint(r.Stdout, "present")
				}
			}
		case "serviceaccounts":
			if scenario == "generated-account-unreadable" && strings.HasPrefix(projection(), "go-template={{.kind}}") {
				fail()
			}
			if args[getIndex+2] != "sample" {
				fail()
			}
			if scenario == "child-serviceaccount-collision" {
				fmt.Fprint(r.Stdout, "present")
			} else if fixtureTag == "0.10.3" && agentExists() {
				if strings.HasPrefix(projection(), "go-template={{.kind}}") {
					fmt.Fprint(r.Stdout, "ServiceAccount\nsample\nagents\nagent-account-uid\n24\nactive\nkagent.dev/v1alpha2|Agent|sample|agent-uid\n")
				} else {
					fmt.Fprint(r.Stdout, "present")
				}
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
			fmt.Fprintf(r.Stdout, `{"apiVersion":"kagent.dev/v1alpha2","kind":"RemoteMCPServer","metadata":{"name":%q,"namespace":"agents","uid":%q,"resourceVersion":"tool-rv","generation":3},"spec":{"description":"tools","protocol":"STREAMABLE_HTTP","url":%q},"status":{"observedGeneration":3,"conditions":[{"type":"Accepted","status":"True","observedGeneration":3}],"secretHash":%q,"discoveredTools":%s}}`, name, uid, endpoint, secretHash, discovered)
		case "modelconfigs.kagent.dev", "agents.kagent.dev":
			name := args[getIndex+2]
			if slices.Contains(args, "--ignore-not-found=true") {
				if scenario == "primary-unreadable" {
					fail()
				}
				if projection() != "json" {
					if scenario == "model-collision" && kind == "modelconfigs.kagent.dev" || fixtureTag == "0.10.3" && func() bool { _, err := os.Stat(filepath.Join(dir, name+"-"+kind+".json")); return err == nil }() {
						fmt.Fprint(r.Stdout, "present")
					}
					exit(0)
				}
				if _, err := os.Stat(filepath.Join(dir, name+"-"+kind+".json")); os.IsNotExist(err) {
					exit(0)
				}
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
				// v0.10.2 stamps ModelConfig.status.observedGeneration, not Accepted's.
				conditions = []any{map[string]any{"type": "Accepted", "status": "True"}}
			}
			status := map[string]any{"observedGeneration": observed, "conditions": conditions}
			if objectKind == "ModelConfig" {
				status["secretHash"] = "model-secret-hash"
			}
			if scenario == "later-live-mutated" && objectKind == "Agent" {
				object["spec"].(map[string]any)["description"] = "mutated"
			}
			if scenario == "model-loses-acceptance" && objectKind == "ModelConfig" {
				marker := filepath.Join(dir, "model-read")
				if _, err := os.Stat(marker); err == nil {
					status["conditions"] = []any{map[string]any{"type": "Accepted", "status": "False"}}
				} else {
					_ = os.WriteFile(marker, []byte("read"), 0600)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "workload-read")); err == nil {
				if scenario == "final-model-rejected" && objectKind == "ModelConfig" || scenario == "final-agent-rejected" && objectKind == "Agent" {
					status["conditions"] = []any{map[string]any{"type": "Accepted", "status": "False", "observedGeneration": generation}}
				}
				if scenario == "final-agent-notready" && objectKind == "Agent" {
					status["conditions"] = []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": generation}, map[string]any{"type": "Ready", "status": "False", "reason": "DeploymentReady", "observedGeneration": generation}}
				}
				if scenario == "final-agent-stale" && objectKind == "Agent" {
					status["observedGeneration"] = 0
				}
			}
			object["status"] = status
			_ = json.NewEncoder(r.Stdout).Encode(object)
		default:
			fail()
		}
		exit(0)
	}
	if slices.Contains(args, "create") {
		if call.Body == nil {
			fail()
		}
		if slices.Contains(args, "--dry-run=server") {
			if scenario == "slow-admission" {
				r.delay(time.Second)
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
			_ = json.NewEncoder(r.Stdout).Encode(body)
			exit(0)
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
		if scenario == "unknown-create" {
			fail()
		}
		body, _ := json.Marshal(call.Body)
		name := metadata["name"].(string)
		plural := kagentPlural(kind)
		if err := os.WriteFile(filepath.Join(dir, name+"-"+plural+".json"), body, 0o600); err != nil {
			fail()
		}
		_, _ = r.Stdout.Write(body)
		exit(0)
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
