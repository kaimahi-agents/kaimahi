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
