package kubectlfake

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// AgentSessions is the create-only Agent v1.1 fixture; no installed command
// selects it. It shares the same request boundary as the executable fixtures.
func AgentSessions(r Request) error {
	return dispatch(r, func(r Request) {
		dir := r.Getenv("KMX_SESSIONS_TEST_DIR")
		if dir == "" {
			exit(2)
		}
		args := r.Args
		if slices.Equal(args, []string{"version", "--client"}) {
			return
		}
		if len(args) < 4 || !slices.Equal(args[:3], []string{"--context", "kind-test", "--request-timeout=10s"}) {
			exit(2)
		}
		args = args[3:]
		body, err := io.ReadAll(r.Stdin)
		if err != nil {
			exit(2)
		}
		log, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			exit(2)
		}
		json.NewEncoder(log).Encode(args)
		log.Close()
		if slices.Equal(args, []string{"config", "view", "-o", "json"}) {
			fmt.Fprint(r.Stdout, `{"clusters":[{"name":"cluster","cluster":{"server":"https://remote.invalid"}}],"contexts":[{"name":"kind-test","context":{"cluster":"cluster"}}],"current-context":"kind-test"}`)
			return
		}
		if len(args) == 3 && args[0] == "get" && args[1] == "--raw" && strings.HasPrefix(args[2], "/api/v1/namespaces/") {
			name := strings.TrimPrefix(args[2], "/api/v1/namespaces/")
			json.NewEncoder(r.Stdout).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name, "uid": name + "-uid"}})
			return
		}
		if len(args) == 3 && args[0] == "get" && args[1] == "--raw" {
			switch args[2] {
			case "/apis":
				fmt.Fprint(r.Stdout, `{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"agents.aks-durable-agents.io","versions":[{"groupVersion":"agents.aks-durable-agents.io/v1alpha1","version":"v1alpha1"}]}]}`)
			case "/apis/agents.aks-durable-agents.io/v1alpha1":
				fmt.Fprint(r.Stdout, `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"agents.aks-durable-agents.io/v1alpha1","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]}]}`)
			default:
				raw, err := os.ReadFile(filepath.Join(dir, "agent.json"))
				if err != nil {
					exit(2)
				}
				if strings.HasSuffix(args[2], "/agents?limit=128") {
					var doc map[string]any
					json.Unmarshal(raw, &doc)
					json.NewEncoder(r.Stdout).Encode(map[string]any{"apiVersion": "agents.aks-durable-agents.io/v1alpha1", "kind": "AgentList", "metadata": map[string]any{"resourceVersion": "10"}, "items": []any{doc}})
				} else if strings.HasPrefix(args[2], "/apis/agents.aks-durable-agents.io/v1alpha1/namespaces/workloads/agents/") {
					fmt.Fprint(r.Stdout, string(raw))
				} else {
					exit(2)
				}
			}
			return
		}
		if len(args) < 6 || args[0] != "-n" || args[1] != "workloads" {
			exit(2)
		}
		path := filepath.Join(dir, "agent.json")
		scenario := r.Getenv("KMX_SESSIONS_TEST_SCENARIO")
		if args[2] == "get" {
			if len(args) != 8 || !slices.Equal(args[5:], []string{"--ignore-not-found=true", "-o", "json"}) || len(body) != 0 || args[3] != "agents.agents.aks-durable-agents.io" {
				exit(2)
			}
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				exit(2)
			}
			var doc map[string]any
			json.Unmarshal(raw, &doc)
			if doc["metadata"].(map[string]any)["name"] != args[4] {
				return
			}
			if scenario == "recreated" {
				doc["metadata"].(map[string]any)["uid"] = "replacement"
			}
			json.NewEncoder(r.Stdout).Encode(doc)
			return
		}
		if !slices.Equal(args[2:], []string{"create", "--validate=strict", "-f", "-", "-o", "json"}) {
			exit(2)
		}
		if _, err := os.Stat(path); err == nil {
			fmt.Fprint(r.Stderr, "AlreadyExists")
			exit(1)
		}
		var doc map[string]any
		if json.Unmarshal(body, &doc) != nil {
			exit(2)
		}
		meta := doc["metadata"].(map[string]any)
		meta["uid"] = "agent-uid"
		meta["generation"] = 1
		meta["resourceVersion"] = "10"
		spec := doc["spec"].(map[string]any)
		reference := spec["package"].(map[string]any)["reference"].(string)
		_, digest, _ := strings.Cut(reference, "@")
		conditions := []any{map[string]any{"type": "PackageVerified", "status": "True", "observedGeneration": 1, "reason": "Verified", "message": "verified", "lastTransitionTime": "2026-10-24T00:00:00Z"}, map[string]any{"type": "Registered", "status": "True", "observedGeneration": 1, "reason": "Admitted", "message": "registered", "lastTransitionTime": "2026-10-24T00:00:00Z"}}
		status := map[string]any{"agentUID": "agent-uid", "observedGeneration": 1, "packageDigest": digest, "member": spec["member"], "servedDigest": digest, "resolvedBindingDigest": "sha256:" + strings.Repeat("b", 64), "revisionDigest": "sha256:" + strings.Repeat("c", 64), "harnessName": "a-greeter", "conditions": conditions}
		doc["status"] = status
		if scenario == "pending" {
			status["conditions"] = []any{}
		}
		if scenario == "unacknowledged" {
			status["agentUID"] = "old"
			status["conditions"] = []any{}
		}
		if scenario == "unknown-spec" {
			spec["admissionExtra"] = true
		}
		if scenario == "changed-binding" {
			spec["modelBindingRef"] = map[string]any{"name": "other"}
		}
		if scenario == "mutated-create" {
			spec["member"] = "other"
		}
		raw, _ := json.Marshal(doc)
		if os.WriteFile(path, raw, 0600) != nil {
			exit(2)
		}
		if scenario == "create-lost" {
			fmt.Fprint(r.Stderr, "PRIVATE failure")
			exit(1)
		}
		fmt.Fprint(r.Stdout, string(raw))
	})
}
