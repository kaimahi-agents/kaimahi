package app

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestDiscoveryTargetMalformedProviderMetadata(t *testing.T) {
	for _, mode := range []string{"cursor-type", "model-type", "generation-fraction", "condition-generation-type", "metadata-null", "duplicate-uid", "no-list-verb"} {
		t.Run(mode, func(t *testing.T) {
			a, p := targetFixture(t)
			list := p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"].(map[string]any)
			obj := list["items"].([]any)[0].(map[string]any)
			switch mode {
			case "cursor-type":
				list["metadata"] = map[string]any{"continue": 7, "resourceVersion": ""}
			case "model-type":
				obj["spec"].(map[string]any)["defaultModel"] = 7
			case "generation-fraction":
				obj["metadata"].(map[string]any)["generation"] = 1.5
			case "condition-generation-type":
				obj["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = "1"
			case "metadata-null":
				list["metadata"] = nil
			case "duplicate-uid":
				b, _ := json.Marshal(obj)
				var dup map[string]any
				json.Unmarshal(b, &dup)
				dup["metadata"].(map[string]any)["name"] = "other"
				list["items"] = []any{obj, dup}
			case "no-list-verb":
				p["/apis/core.orka.ai/v1alpha1"].(map[string]any)["resources"].([]any)[1].(map[string]any)["verbs"] = []string{"get"}
			}
			r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
			if e == nil || r.Complete {
				t.Fatalf("accepted %s: %+v", mode, r)
			}
		})
	}
}
func TestDiscoveryReviewRuntimeReasons(t *testing.T) {
	for _, mode := range []string{"unready", "login", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			a, p := targetFixture(t)
			path := "/apis/apps/v1/namespaces/orka-system/deployments?limit=64"
			list := p[path].(map[string]any)
			switch mode {
			case "unready":
				list["items"].([]any)[0].(map[string]any)["status"].(map[string]any)["readyReplicas"] = 0
			case "login":
				p[path] = func(c *exec.Cmd) error {
					fmt.Fprint(c.Stderr, "error: You must be logged in to the server")
					return fmt.Errorf("fail")
				}
			case "unrelated":
				d := list["items"].([]any)[0].(map[string]any)
				d["metadata"].(map[string]any)["name"] = "unrelated"
				d["metadata"].(map[string]any)["labels"] = map[string]any{}
				d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "unrelated"
			}
			r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
			if e == nil || r.Complete {
				t.Fatal(r)
			}
			switch mode {
			case "unready":
				for _, c := range r.Checks {
					if c.Reason == "unsupported_runtime" {
						t.Fatal(r.Checks)
					}
				}
				for _, row := range r.Target.Tools.Tools {
					if row.Reason == "unsupported_runtime" {
						t.Fatal(row)
					}
				}
			case "login":
				if r.Target.Runtime.Reason != "unauthorized" {
					t.Fatal(r.Target.Runtime)
				}
			case "unrelated":
				if r.Target.Runtime.State != "missing" {
					t.Fatal(r.Target.Runtime)
				}
			}
		})
	}
}

func TestDiscoveryTargetRuntimeBracketSpansComposition(t *testing.T) {
	a, p := targetFixture(t)
	path := "/api/v1/namespaces/orka-system/serviceaccounts/orka-ai-worker"
	old := p[path]
	n := 0
	p[path] = func(c *exec.Cmd) error {
		n++
		b, _ := json.Marshal(old)
		if n >= 3 {
			fmt.Fprint(c.Stdout, strings.ReplaceAll(string(b), "worker-uid", "changed"))
		} else {
			c.Stdout.Write(b)
		}
		return nil
	}
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e == nil || r.Complete || r.Target.Runtime.State == "available" {
		t.Fatal(r.Target, e, n)
	}
}

func TestDiscoveryTargetPartialProviderSort(t *testing.T) {
	a, p := targetFixture(t)
	path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/providers?limit=64"
	list := p[path].(map[string]any)
	obj := list["items"].([]any)[0].(map[string]any)
	b, _ := json.Marshal(obj)
	var dup map[string]any
	json.Unmarshal(b, &dup)
	dup["metadata"].(map[string]any)["name"] = "aaa"
	dup["metadata"].(map[string]any)["uid"] = "other"
	list["items"] = []any{obj, dup}
	list["metadata"].(map[string]any)["continue"] = "later"
	p[path+"&continue=later"] = func(c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "Error from server (Forbidden): private")
		return fmt.Errorf("fail")
	}
	r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
	if e == nil || len(r.Target.Providers) != 2 || r.Target.Providers[0].Name != "aaa" {
		t.Fatal(r.Target, e)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "private-content-canary") {
		t.Fatal(string(raw))
	}
}
