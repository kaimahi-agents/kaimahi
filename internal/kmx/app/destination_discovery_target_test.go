package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
	"os/exec"
	"strings"
	"testing"
)

func targetFixture(t *testing.T) (*App, map[string]any) {
	t.Helper()
	a, p, _ := toolFixture(t)
	original := a.captureCommand
	for kind, raw := range readFixtureCRDs(t) {
		var m map[string]any
		if e := yaml.Unmarshal(raw, &m); e != nil {
			t.Fatal(e)
		}
		p["/apis/apiextensions.k8s.io/v1/customresourcedefinitions/"+orkaPlural(kind)] = m
	}
	api := p["/apis/core.orka.ai/v1alpha1"].(map[string]any)
	api["resources"] = append(api["resources"].([]any), map[string]any{"name": "providers", "kind": "Provider", "namespaced": true, "verbs": []string{"get", "list"}})
	provider := map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": map[string]any{"name": "local", "namespace": "orka-system", "uid": "provider-uid", "generation": 1}, "spec": map[string]any{"type": "openai", "defaultModel": "declared-model", "baseURL": "https://private-content-canary.invalid", "secretRef": map[string]any{"name": "private-content-canary"}}, "status": map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 1, "message": "private-content-canary"}}}}
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"] = map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "ProviderList", "metadata": map[string]any{"resourceVersion": "1"}, "items": []any{provider}}
	p["/api/v1/namespaces/orka-system/serviceaccounts/orka-ai-worker"] = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "orka-ai-worker", "namespace": "orka-system", "uid": "worker-uid", "labels": map[string]any{"helm.sh/chart": "orka-0.3.0", "app.kubernetes.io/instance": "orka", "app.kubernetes.io/name": "orka", "app.kubernetes.io/managed-by": "Helm", "orka.ai/worker": "true", "orka.ai/worker-trust": "ai"}}}
	a.captureCommand = func(ctx context.Context, c *exec.Cmd) error {
		path := c.Args[len(c.Args)-1]
		if strings.HasPrefix(path, "/api/v1/namespaces?") || strings.Contains(path, "secrets") || strings.Contains(path, "token") {
			t.Fatal("forbidden read", path)
		}
		return original(ctx, c)
	}
	return a, p
}
func TestDiscoveryTargetExplicitNamespaceNeedsNoNamespaceList(t *testing.T) {
	a, _ := targetFixture(t)
	a.Cfg = nil
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e != nil || !r.Complete || r.Scope != "target" || r.Target == nil || r.Target.NamespaceUID != "namespace-uid" || r.Target.ClusterUID != "cluster-uid" || r.Target.Runtime.State != "available" {
		t.Fatal(r, e)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "private-content-canary") {
		t.Fatal(string(b))
	}
}
func TestDiscoveryTargetRuntimeAndSchemas(t *testing.T) {
	for _, mode := range []string{"unready", "foreign", "worker", "schema"} {
		t.Run(mode, func(t *testing.T) {
			a, p := targetFixture(t)
			if mode == "schema" {
				base := a.captureCommand
				a.captureCommand = func(ctx context.Context, c *exec.Cmd) error {
					if strings.Contains(c.Args[len(c.Args)-1], "customresourcedefinitions/") {
						fmt.Fprint(c.Stdout, `{}`)
						return nil
					}
					return base(ctx, c)
				}
			} else if mode == "worker" {
				p["/api/v1/namespaces/orka-system/serviceaccounts/orka-ai-worker"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)["orka.ai/worker-trust"] = "other"
			} else {
				d := p["/apis/apps/v1/namespaces/orka-system/deployments?limit=64"].(map[string]any)["items"].([]any)[0].(map[string]any)
				if mode == "unready" {
					d["status"].(map[string]any)["readyReplicas"] = 0
				} else {
					d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "foreign"
				}
			}
			r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
			if e == nil || r.Complete || r.Target.Runtime.State == "available" {
				t.Fatal(mode, r, e)
			}
			if mode == "unready" && (r.Target.Runtime.State != "unknown" || r.Target.Runtime.Reason != "read_unknown") {
				t.Fatal(r.Target.Runtime)
			}
		})
	}
}
func TestDiscoveryTargetProviderDeclaration(t *testing.T) {
	a, p := targetFixture(t)
	list := p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"].(map[string]any)
	provider := list["items"].([]any)[0].(map[string]any)
	for _, state := range []string{"True", "False", "Unknown"} {
		provider["status"].(map[string]any)["ready"] = state == "True"
		provider["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["status"] = state
		r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
		if e != nil || r.Target.Providers[0].ConditionState != state || r.Target.Providers[0].Model != "declared-model" {
			t.Fatal(r, e)
		}
	}
	list["items"] = []any{}
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e != nil || !r.Complete || len(r.Target.Providers) != 0 {
		t.Fatal(r, e)
	}
}
func TestDiscoveryTargetUsesSharedToolInventory(t *testing.T) {
	a, _ := targetFixture(t)
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e != nil || r.Target.Tools == nil || !r.Target.Tools.Complete || len(r.Target.Tools.Tools) != 8 || r.Target.Tools.Tools[0].Name != "web_search" {
		t.Fatal(r, e)
	}
}
func TestDiscoveryTargetIdentityBracket(t *testing.T) {
	a, p := targetFixture(t)
	path := "/api/v1/namespaces/orka-system"
	old := p[path]
	n := 0
	p[path] = func(c *exec.Cmd) error {
		n++
		b, _ := json.Marshal(old)
		if n >= 4 {
			fmt.Fprint(c.Stdout, strings.ReplaceAll(string(b), "namespace-uid", "changed"))
		} else {
			c.Stdout.Write(b)
		}
		return nil
	}
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e == nil || r.Complete || r.Target.Tools != nil || len(r.Target.Providers) != 0 || r.Checks[len(r.Checks)-1].Reason != "stale_input" {
		t.Fatal(r, e)
	}
}
func TestDiscoveryTargetAggregateProjection(t *testing.T) {
	a, p := targetFixture(t)
	path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"
	list := p[path].(map[string]any)
	rows := []any{}
	for i := 0; i < 129; i++ {
		rows = append(rows, map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("provider-%03d", i), "namespace": "orka-system", "uid": fmt.Sprintf("uid-%d", i), "generation": 1}, "spec": map[string]any{"type": "openai"}})
	}
	list["items"] = rows
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	b, _ := json.Marshal(r)
	if e == nil || r.Complete || len(b) > 60<<10 || len(r.Target.Providers) > 128 {
		t.Fatal(len(b), r, e)
	}
	p[path] = func(c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "Error from server (Forbidden): private")
		return errors.New("private")
	}
	r, e = a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e == nil || r.Complete || r.Target.NamespaceUID == "" {
		t.Fatal(r, e)
	}
}
