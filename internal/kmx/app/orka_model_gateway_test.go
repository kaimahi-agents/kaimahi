package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func modelFixtureChildren(objects map[string]map[string]any) []map[string]any {
	for _, root := range objects {
		if root["kind"] != "Deployment" {
			continue
		}
		meta := modelMap(root["metadata"])
		name := modelText(meta["name"])
		raw, _ := json.Marshal(modelMap(modelMap(root["spec"])["template"])["spec"])
		var podSpec map[string]any
		json.Unmarshal(raw, &podSpec)
		podSpec["nodeName"] = "test-control-plane"
		labels := modelMap(modelMap(modelMap(root["spec"])["selector"])["matchLabels"])
		rs := map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": name + "-rs", "namespace": OrkaNamespace, "uid": "owned-rs", "labels": labels, "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": name, "uid": meta["uid"], "controller": true}}}, "spec": map[string]any{"template": modelMap(root["spec"])["template"]}}
		pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name + "-pod", "namespace": OrkaNamespace, "uid": "owned-pod", "labels": labels, "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": name + "-rs", "uid": "owned-rs", "controller": true}}}, "spec": podSpec, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}
		return []map[string]any{rs, pod}
	}
	return nil
}

func modelConfigureFixture(t *testing.T) (*App, map[string]map[string]any, *[]string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("model gateway is guest Linux only")
	}
	a, pages, _ := toolFixture(t)
	local, _, _, log := offlineClusterApp(t, "docker")
	a.Cfg, a.Run = local.Cfg, local.Run
	a.Run.Env = append(a.Run.Env, "KUBERC=off", "KUBECTL_KUBERC=false")
	a.Run.Unset = append(a.Run.Unset, "KUBERC", "KUBECTL_KUBERC")
	t.Setenv("OFFLINE_CONTEXT_MISSING", "")
	t.Setenv("MODEL_CRI_RUNNING", "")
	a.guarded = true
	bin := filepath.Dir(log)
	script := `#!/bin/sh
case "$0 $*" in
*/findmnt*) printf 'tmpfs rw,nosuid,nodev,noexec\n';;
*/stat*copilot-github-token*|*/stat*model-api-key*) printf 'regular file:0:65532:640:1:12\n';;
*/stat*credentials*) printf 'directory:0:65532:750:1:2\n';;
*/stat*) printf 'directory:0:0:755:1:1\n';;
*docker*'context inspect'*) printf '%s' "$OFFLINE_DOCKER_CONTEXT_JSON";;
*docker*'exec'*'crictl ps'*) if [ -n "$MODEL_CRI_RUNNING" ]; then printf '{"containers":[{"state":"CONTAINER_RUNNING","labels":{"io.kubernetes.pod.uid":"owned-pod","io.kubernetes.pod.namespace":"orka-system","io.kubernetes.pod.name":"copilot-vekil-pod"}}]}'; else printf '{"containers":[]}'; fi;;
*docker*'exec'*findmnt*) printf 'tmpfs ro,nosuid,nodev,noexec\n';;
*docker*'exec'*stat*) printf 'directory:0:65532:750:1:2\n';;
*kind*'get nodes'*) printf 'test-control-plane\n';;
*docker*'ps -a'*) printf 'test-control-plane\n';;
*docker*'container inspect'*) printf '[{"Name":"/test-control-plane","Image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Config":{"Labels":{"io.x-k8s.kind.cluster":"test"}},"State":{"Running":true},"Mounts":[{"Type":"bind","Source":"/run/bridge/credentials","Destination":"/run/bridge/credentials","RW":false,"Propagation":"rprivate"}]}]';;
*) exit 7;; esac
`
	for _, name := range []string{"findmnt", "stat", "docker"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	a.Cfg.ContainerEngine = "docker"
	a.Cfg.KindCluster = "test"
	objects := map[string]map[string]any{}
	calls := []string{}
	pages["/api/v1/namespaces/orka-system/serviceaccounts/default"] = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "default", "namespace": OrkaNamespace, "uid": "default-sa"}}
	pages["/api/v1/nodes?limit=64"] = map[string]any{"apiVersion": "v1", "kind": "NodeList", "metadata": map[string]any{}, "items": []any{map[string]any{"metadata": map[string]any{"name": "test-control-plane"}, "status": map[string]any{"nodeInfo": map[string]any{"architecture": "amd64"}}}}}
	a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		calls = append(calls, args)
		if !slices.Contains(cmd.Env, "KUBERC=off") || !slices.Contains(cmd.Env, "KUBECTL_KUBERC=false") {
			t.Fatal("kuberc not pinned")
		}
		if strings.Contains(args, " create ") {
			b, _ := io.ReadAll(cmd.Stdin)
			var obj map[string]any
			json.Unmarshal(b, &obj)
			if strings.Contains(args, "--dry-run=server") {
				return json.NewEncoder(cmd.Stdout).Encode(obj)
			}
			meta := modelMap(obj["metadata"])
			meta["uid"] = "owned-" + modelText(obj["kind"])
			meta["resourceVersion"] = "3"
			if obj["kind"] == "Deployment" || obj["kind"] == "Provider" {
				meta["generation"] = float64(1)
			}
			r, _ := modelDesired(obj)
			p, _ := modelPath(OrkaNamespace, r)
			objects[p] = obj
			return json.NewEncoder(cmd.Stdout).Encode(obj)
		}
		i := slices.Index(cmd.Args, "--raw")
		if i >= 0 {
			path := cmd.Args[i+1]
			if value, ok := pages[path]; ok {
				return json.NewEncoder(cmd.Stdout).Encode(value)
			}
			if strings.HasSuffix(path, "/readyz") {
				fmt.Fprint(cmd.Stdout, `{"status":"ready"}`)
				return nil
			}
			if strings.HasSuffix(path, "/v1/models") || strings.HasSuffix(path, "/v1/models?limit=1000") {
				fmt.Fprint(cmd.Stdout, `{"data":[{"id":"gpt-4.1"}]}`)
				return nil
			}
			if obj, ok := objects[path]; ok {
				return json.NewEncoder(cmd.Stdout).Encode(obj)
			}
			children := modelFixtureChildren(objects)
			if strings.Contains(path, "replicasets?limit=64") || strings.Contains(path, "pods?limit=64") {
				kind, version := "Pod", "v1"
				if strings.Contains(path, "replicasets") {
					kind, version = "ReplicaSet", "apps/v1"
				}
				items := []any{}
				for _, child := range children {
					if child["kind"] == kind {
						items = append(items, child)
					}
				}
				return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"apiVersion": version, "kind": kind + "List", "metadata": map[string]any{}, "items": items})
			}
			for _, child := range children {
				if strings.HasSuffix(path, "/"+modelText(modelMap(child["metadata"])["name"])) {
					return json.NewEncoder(cmd.Stdout).Encode(child)
				}
			}
			return fmt.Errorf("unhandled fixture request")
		}
		if slices.Contains(cmd.Args, "--ignore-not-found=true") {
			get := slices.Index(cmd.Args, "get")
			resource, name := cmd.Args[get+1], cmd.Args[get+2]
			for _, child := range modelFixtureChildren(objects) {
				qualified := "pods"
				if child["kind"] == "ReplicaSet" {
					qualified = "replicasets.apps"
				}
				if resource == qualified && modelMap(child["metadata"])["name"] == name {
					fmt.Fprint(cmd.Stdout, modelMap(child["metadata"])["uid"])
					return nil
				}
			}
			for path, obj := range objects {
				api, _ := modelAPIFor(modelText(obj["kind"]))
				qualified := api.plural
				if group, _, grouped := strings.Cut(api.version, "/"); grouped {
					qualified += "." + group
				}
				if resource == qualified && strings.HasSuffix(path, "/"+api.plural+"/"+name) {
					fmt.Fprint(cmd.Stdout, modelMap(obj["metadata"])["uid"])
					break
				}
			}
			return nil
		}
		return fmt.Errorf("unexpected fixture command")
	}
	return a, objects, &calls
}
