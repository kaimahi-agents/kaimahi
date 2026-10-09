package kubectl

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// The subprocess represents the API boundary. State is persisted between calls
// and between deploys so retries exercise actual read/compare/write sequencing.
func TestReconcileKubectlHelper(t *testing.T) {
	dir := os.Getenv("KMX_RECONCILE_DIR")
	if dir == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Equal(args, []string{"version", "--client"}) {
		os.Exit(0)
	}
	fail := func() { fmt.Fprint(os.Stderr, "private-token-must-not-escape"); os.Exit(1) }
	if len(args) < 2 || args[0] != "--context" || args[1] != "kind-test" {
		fail()
	}
	call := orkaCall{Args: args}
	if slices.Contains(args, "-f") {
		raw, _ := io.ReadAll(os.Stdin)
		if json.Unmarshal(raw, &call.Document) != nil {
			fail()
		}
	}
	if pi := slices.Index(args, "-p"); pi >= 0 && pi+1 < len(args) {
		if json.Unmarshal([]byte(args[pi+1]), &call.Patch) != nil {
			fail()
		}
	}
	log, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_ = json.NewEncoder(log).Encode(call)
	_ = log.Close()
	if os.Getenv("KMX_LIFT_POLICY_TEST") == "1" && slices.Contains(args, "--raw") {
		if os.Getenv("KMX_LIFT_POLICY_API_FAIL") == "1" {
			fail()
		}
		fmt.Print(getenvLiftTest("KMX_LIFT_POLICY_REVIEW", `{"status":{"allowed":false,"denied":true}}`))
		os.Exit(0)
	}
	if slices.Contains(args, "config") {
		server := "https://127.0.0.1:6443"
		if os.Getenv("KMX_RECONCILE_REMOTE") == "1" {
			server = "https://managed.example.invalid"
		}
		fmt.Printf(`{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":%q}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`, server)
		os.Exit(0)
	}
	if os.Getenv("KMX_LIFT_TEST") == "1" && slices.Contains(args, "rollout") {
		expected := getenvLiftTest("KMX_LIFT_CONTROLLER_NAME", "orka-controller-manager")
		if !slices.Contains(args, "deploy/"+expected) {
			fail()
		}
		if slices.Contains(strings.Split(os.Getenv("KMX_LIFT_MISSING"), ","), "controller") {
			fail()
		}
		fmt.Print("deployment successfully rolled out")
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 {
		kind, name := args[i+1], args[i+2]
		if name == "--all-namespaces" {
			raw, err := os.ReadFile(filepath.Join(dir, "list-"+kind+".json"))
			if os.IsNotExist(err) {
				raw = []byte(`{"items":[]}`)
			} else if err != nil {
				fail()
			}
			var list map[string]any
			if json.Unmarshal(raw, &list) != nil {
				fail()
			}
			output := slices.IndexFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "-o=go-template=") })
			if output < 0 {
				fail()
			}
			projection, err := template.New("dependents").Parse(strings.TrimPrefix(args[output], "-o=go-template="))
			if err != nil || projection.Execute(os.Stdout, list) != nil {
				fail()
			}
			os.Exit(0)
		}
		if os.Getenv("KMX_STATUS_FORBIDDEN") == "1" && kind == "agents.core.orka.ai" {
			fail()
		}
		if os.Getenv("KMX_LIFT_POLICY_TEST") == "1" && kind == "serviceaccounts" {
			fmt.Printf(`{"items":[%s]}`, workerAccount("orka-ai-worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"))
			os.Exit(0)
		}
		if os.Getenv("KMX_LIFT_TEST") == "1" {
			switch kind {
			case "deploy":
				if name != "-o" {
					fail()
				}
				selected := getenvLiftTest("KMX_LIFT_CONTROLLER_NAME", "orka-controller-manager")
				labels := map[string]string{"app.kubernetes.io/name": "orka"}
				if selected == "orka-controller-manager" {
					labels["control-plane"] = "controller-manager"
				} else {
					labels["app.kubernetes.io/component"] = "controller"
				}
				if os.Getenv("KMX_LIFT_POLICY_TEST") == "1" {
					fmt.Printf(`{"items":[%s]}`, workerController("orka-controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=orka-ai-worker"]`))
					os.Exit(0)
				}
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"name": selected, "labels": labels}}}})
				os.Exit(0)
			case "namespace", "namespaces":
				if name == "kube-system" {
					uid := getenvLiftTest("KMX_LIFT_CLUSTER_UID", "cluster-uid")
					if _, err := os.Stat(filepath.Join(dir, "repointed-after-preflight")); err == nil {
						uid = "repointed-uid"
					}
					if path := os.Getenv("KMX_RETIRE_SELECTION_BLOCK"); path != "" {
						_ = os.Remove(path)
						_ = os.Mkdir(path, 0700)
						_ = os.WriteFile(filepath.Join(path, "block"), nil, 0600)
					}
					fmt.Printf(`{"kind":"Namespace","metadata":{"name":"kube-system","uid":%q}}`, uid)
					os.Exit(0)
				}
				if slices.Contains(strings.Split(os.Getenv("KMX_LIFT_MISSING"), ","), "namespace") {
					fail()
				}
				fmt.Printf(`{"kind":"Namespace","metadata":{"name":%q,"uid":"namespace-uid"}}`, name)
				os.Exit(0)
			case "providers.core.orka.ai":
				if name == "inference" || name == "sample" && os.Getenv("KMX_LIFT_SELECTED_SAME") == "1" {
					if os.Getenv("KMX_LIFT_MISSING") == "provider" {
						os.Exit(0)
					}
					ready := os.Getenv("KMX_LIFT_MISSING") != "provider-ready"
					reportedReady := ready && os.Getenv("KMX_LIFT_STATUS_NOT_READY") != "1"
					key := "api-key"
					if os.Getenv("KMX_LIFT_MISSING") == "provider-key" {
						key = ""
					}
					if os.Getenv("KMX_LIFT_SELECTED_AZURE") == "1" {
						fmt.Printf(`{"kind":"Provider","metadata":{"name":%q,"namespace":"orka-system","uid":"inference-uid","generation":1},"spec":{"type":"azure-openai","baseURL":"https://target.openai.azure.com","defaultModel":"chat-prod","azure":{"deploymentName":%q,"apiVersion":"2024-02-15-preview"},"secretRef":{"name":"target-secret","key":%q}},"status":{"ready":%t,"conditions":[{"type":"Ready","status":%q,"observedGeneration":1}]}}`, name, getenvLiftTest("KMX_LIFT_AZURE_DEPLOYMENT", "chat-prod"), key, reportedReady, map[bool]string{true: "True", false: "False"}[ready])
						os.Exit(0)
					}
					fmt.Printf(`{"kind":"Provider","metadata":{"name":%q,"namespace":"orka-system","uid":"inference-uid","generation":1},"spec":{"type":"openai","baseURL":"https://target.example.invalid/v1","defaultModel":"target-default","secretRef":{"name":"target-secret","key":%q}},"status":{"ready":%t,"conditions":[{"type":"Ready","status":%q,"observedGeneration":1}]}}`, name, key, reportedReady, map[bool]string{true: "True", false: "False"}[ready])
					os.Exit(0)
				}
			case "agents.core.orka.ai":
				if name != "sample" {
					switch os.Getenv("KMX_LIFT_MISSING") {
					case "allowed-agent-read":
						fail()
					case "allowed-agent":
						os.Exit(0)
					}
					fmt.Printf(`{"kind":"Agent","metadata":{"name":%q,"namespace":"orka-system","uid":"helper-uid","generation":1}}`, name)
					os.Exit(0)
				}
			case "tools.core.orka.ai":
				if os.Getenv("KMX_LIFT_REPOINT_AFTER_PREFLIGHT") == "1" {
					_ = os.WriteFile(filepath.Join(dir, "repointed-after-preflight"), nil, 0600)
				}
				if name == "read" && os.Getenv("KMX_LIFT_TOOL_SPEC_READ") != "" {
					fmt.Printf(`{"kind":"Tool","metadata":{"name":%q,"namespace":"orka-system","generation":1},"spec":%s,"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}`, name, os.Getenv("KMX_LIFT_TOOL_SPEC_READ"))
					os.Exit(0)
				}
				if spec := os.Getenv("KMX_LIFT_TOOL_SPEC"); spec != "" {
					fmt.Printf(`{"kind":"Tool","metadata":{"name":%q,"namespace":"orka-system","generation":1},"spec":%s,"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}`, name, spec)
					os.Exit(0)
				}
				if os.Getenv("KMX_LIFT_MISSING") == "tool" || os.Getenv("KMX_LIFT_MISSING") == "tool-available" && name == "search" {
					if os.Getenv("KMX_LIFT_MISSING") == "tool" {
						os.Exit(0)
					}
					fmt.Printf(`{"kind":"Tool","metadata":{"name":%q,"namespace":"orka-system","generation":1},"status":{"conditions":[{"type":"Available","status":"False","observedGeneration":1}]}}`, name)
					os.Exit(0)
				}
				fmt.Printf(`{"kind":"Tool","metadata":{"name":%q,"namespace":"orka-system","generation":1},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}`, name)
				os.Exit(0)
			case "secret":
				if os.Getenv("KMX_LIFT_MISSING") == "secret" {
					os.Exit(0)
				}
			case "crd":
				if strings.HasPrefix(name, "tools.") {
					if slices.Contains(strings.Split(os.Getenv("KMX_LIFT_MISSING"), ","), "crd") {
						os.Exit(0)
					}
					fmt.Print("customresourcedefinition.apiextensions.k8s.io/tools.core.orka.ai")
					os.Exit(0)
				}
			}
		}
		if kind == "crd" {
			plural, _, _ := strings.Cut(name, ".")
			raw, e := os.ReadFile(filepath.Join(os.Getenv("KMX_RECONCILE_FIXTURES"), "v0.1.3", plural+".yaml"))
			if e != nil {
				fail()
			}
			if plural == "agents" && os.Getenv("KMX_LIFT_MISSING") == "coordination-crd" {
				var crd map[string]any
				if yaml.Unmarshal(raw, &crd) != nil {
					fail()
				}
				versions := crd["spec"].(map[string]any)["versions"].([]any)
				schema := versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
				agentSpec := schema["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
				delete(agentSpec, "coordination")
				if json.NewEncoder(os.Stdout).Encode(crd) != nil {
					fail()
				}
				os.Exit(0)
			}
			_, _ = os.Stdout.Write(raw)
			os.Exit(0)
		}
		if kind == "secret" {
			fmt.Print("secret\npresent")
			os.Exit(0)
		}
		raw, e := os.ReadFile(filepath.Join(dir, kind+".json"))
		if os.IsNotExist(e) && slices.Contains(args, "--ignore-not-found=true") {
			os.Exit(0)
		}
		if e != nil {
			fail()
		}
		var obj map[string]any
		if json.Unmarshal(raw, &obj) != nil {
			fail()
		}
		if slices.Contains(args, "name") {
			fmt.Print(kind + "/" + name)
			os.Exit(0)
		}
		meta := obj["metadata"].(map[string]any)
		if os.Getenv("KMX_RECONCILE_READY_REGRESSION") == "1" && kind == "agents.core.orka.ai" && !slices.Contains(args, "--ignore-not-found=true") {
			_ = os.WriteFile(filepath.Join(dir, "provider-not-ready"), nil, 0600)
		}
		if os.Getenv("KMX_RECONCILE_STEAL") == obj["kind"] && !slices.Contains(args, "--ignore-not-found=true") {
			meta["annotations"].(map[string]any)["kaimahi.dev/bundle"] = "other-bundle"
			changed, _ := json.Marshal(obj)
			_ = os.WriteFile(filepath.Join(dir, kind+".json"), changed, 0600)
		}
		obj["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": meta["generation"]}}}
		if kind == "providers.core.orka.ai" {
			if _, err := os.Stat(filepath.Join(dir, "provider-not-ready")); err == nil {
				obj["status"] = map[string]any{"ready": false, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "observedGeneration": meta["generation"]}}}
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(obj)
		os.Exit(0)
	}
	if ki := slices.Index(args, "patch"); ki >= 0 {
		if !slices.Contains(args, "--type=json") || call.Patch == nil {
			fail()
		}
		kind, name := args[ki+1], args[ki+2]
		path := filepath.Join(dir, kind+".json")
		raw, e := os.ReadFile(path)
		if e != nil {
			fail()
		}
		var live map[string]any
		if json.Unmarshal(raw, &live) != nil {
			fail()
		}
		meta, _ := live["metadata"].(map[string]any)
		if meta == nil || meta["name"] != name {
			fail()
		}
		if len(call.Patch) >= 5 && call.Patch[0]["op"] == "test" && call.Patch[1]["path"] == "/metadata/uid" {
			if os.Getenv("KMX_RETIRE_FAIL_PATCH_ONCE") == kind {
				flag := filepath.Join(dir, "retire-failed-"+kind)
				if _, err := os.Stat(flag); os.IsNotExist(err) {
					_ = os.WriteFile(flag, nil, 0600)
					fail()
				}
			}
			if call.Patch[0]["value"] != meta["resourceVersion"] || call.Patch[1]["value"] != meta["uid"] {
				fail()
			}
			annotations, _ := meta["annotations"].(map[string]any)
			for _, op := range call.Patch[2:] {
				key := strings.NewReplacer("~1", "/", "~0", "~").Replace(strings.TrimPrefix(fmt.Sprint(op["path"]), "/metadata/annotations/"))
				if op["op"] != "remove" || !strings.HasPrefix(fmt.Sprint(op["path"]), "/metadata/annotations/") {
					fail()
				}
				if _, ok := annotations[key]; !ok {
					fail()
				}
				delete(annotations, key)
			}
			meta["resourceVersion"] = "2"
			body, _ := json.Marshal(live)
			if os.WriteFile(path, body, 0600) != nil {
				fail()
			}
			_, _ = os.Stdout.Write(body)
			os.Exit(0)
		}
		// Every marker-refresh patch is exactly: a resourceVersion test
		// precondition, then replace ops on only the two digest annotations.
		// Anything else is refused, matching the real API server rejecting an
		// unexpected or malformed patch.
		allowedPaths := map[string]bool{
			"/metadata/annotations/" + orkaAnnotationPointer(orkaPortableMarker): true,
			"/metadata/annotations/" + orkaAnnotationPointer(orkaRenderedMarker): true,
		}
		if len(call.Patch) != 3 || call.Patch[0]["op"] != "test" || call.Patch[0]["path"] != "/metadata/resourceVersion" {
			fail()
		}
		if fmt.Sprint(call.Patch[0]["value"]) != fmt.Sprint(meta["resourceVersion"]) {
			fail()
		}
		annotations, _ := meta["annotations"].(map[string]any)
		if annotations == nil {
			fail()
		}
		for _, op := range call.Patch[1:] {
			path, _ := op["path"].(string)
			if op["op"] != "replace" || !allowedPaths[path] {
				fail()
			}
			key := strings.NewReplacer("~1", "/", "~0", "~").Replace(strings.TrimPrefix(path, "/metadata/annotations/"))
			annotations[key] = op["value"]
		}
		meta["resourceVersion"] = "2"
		body, _ := json.Marshal(live)
		if os.WriteFile(path, body, 0600) != nil {
			fail()
		}
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	if i := slices.Index(args, "delete"); i >= 0 {
		kind, name := args[i+1], args[i+2]
		path := filepath.Join(dir, kind+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			fail()
		}
		var live map[string]any
		if json.Unmarshal(raw, &live) != nil {
			fail()
		}
		meta := live["metadata"].(map[string]any)
		if meta["name"] != name || !slices.Contains(args, "--resource-version="+fmt.Sprint(meta["resourceVersion"])) {
			fail()
		}
		if os.Remove(path) != nil {
			fail()
		}
		fmt.Print("deleted")
		os.Exit(0)
	}
	if call.Document == nil {
		fail()
	}
	kind := call.Document["kind"].(string)
	path := filepath.Join(dir, strings.ToLower(kind)+"s.core.orka.ai.json")
	meta := call.Document["metadata"].(map[string]any)
	if slices.Contains(args, "replace") {
		raw, e := os.ReadFile(path)
		if e != nil {
			fail()
		}
		var live map[string]any
		if json.Unmarshal(raw, &live) != nil {
			fail()
		}
		current := live["metadata"].(map[string]any)
		if !slices.Contains(args, "--dry-run=server") && os.Getenv("KMX_RECONCILE_RACE") == kind {
			// Simulate a write after Deploy's read but before its conditional replace.
			current["resourceVersion"] = "concurrent-version"
			changed, _ := json.Marshal(live)
			_ = os.WriteFile(path, changed, 0600)
		}
		if meta["resourceVersion"] != current["resourceVersion"] {
			fail()
		}
		// Admission defaults an omitted field on both dry-run and real replace.
		if defaulted, ok := live["spec"].(map[string]any)["rateLimit"]; ok {
			call.Document["spec"].(map[string]any)["rateLimit"] = defaulted
		}
		if kind == "Provider" {
			liveAzure, _ := live["spec"].(map[string]any)["azure"].(map[string]any)
			candidateAzure, _ := call.Document["spec"].(map[string]any)["azure"].(map[string]any)
			if candidateAzure != nil && liveAzure["apiVersion"] != nil && candidateAzure["apiVersion"] == nil {
				candidateAzure["apiVersion"] = liveAzure["apiVersion"]
			}
		}
		if slices.Contains(args, "--dry-run=server") {
			_ = json.NewEncoder(os.Stdout).Encode(call.Document)
			os.Exit(0)
		}
		if os.Getenv("KMX_RECONCILE_FAIL_ONCE") == kind {
			flag := filepath.Join(dir, "failed-"+kind)
			if _, e := os.Stat(flag); os.IsNotExist(e) {
				_ = os.WriteFile(flag, []byte("once"), 0600)
				fail()
			}
		}
		meta["resourceVersion"] = "2"
		if fmt.Sprint(live["spec"]) != fmt.Sprint(call.Document["spec"]) {
			meta["generation"] = current["generation"].(float64) + 1
		}
	} else if slices.Contains(args, "create") {
		if slices.Contains(args, "--dry-run=server") {
			fmt.Print("{}")
			os.Exit(0)
		}
		if _, e := os.Stat(path); e == nil {
			fail()
		}
		if os.Getenv("KMX_RECONCILE_FAIL_ONCE") == kind {
			flag := filepath.Join(dir, "failed-"+kind)
			if _, e := os.Stat(flag); os.IsNotExist(e) {
				_ = os.WriteFile(flag, []byte("once"), 0600)
				fail()
			}
		}
		meta["uid"] = strings.ToLower(kind) + "-uid"
		meta["generation"] = 1
		meta["resourceVersion"] = "1"
	} else {
		fail()
	}
	body, _ := json.Marshal(call.Document)
	if os.WriteFile(path, body, 0600) != nil {
		fail()
	}
	_, _ = os.Stdout.Write(body)
	os.Exit(0)
}
