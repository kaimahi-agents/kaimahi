package app

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestOrkaToolsExactReadRequiresIdentity(t *testing.T) {
	a, p, _ := toolFixture(t)
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = toolPage()
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools/requested"] = toolObject("requested", "GET") // lacks exact-object GVK
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: []OrkaToolRequirement{{Kind: "http", Name: "requested", Enabled: true}}})
	if err == nil || got.Complete || got.Tools[0].Availability != "unknown" {
		t.Fatalf("accepted untyped exact object: %+v %v", got, err)
	}
}
func TestOrkaToolsExactReadsShareByteBudget(t *testing.T) {
	a, p, _ := toolFixture(t)
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = toolPage()
	req := []OrkaToolRequirement{}
	for _, name := range []string{"first", "second", "third"} {
		obj := toolObject(name, "GET")
		obj["kind"], obj["apiVersion"] = "Tool", "core.orka.ai/v1alpha1"
		obj["unrepresented"] = strings.Repeat("x", 3<<20)
		p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools/"+name] = obj
		req = append(req, OrkaToolRequirement{Kind: "http", Name: name, Enabled: true})
	}
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: req})
	if err == nil || got.Complete {
		t.Fatalf("accepted unbounded aggregate reads: %+v %v", got, err)
	}
}
func TestOrkaToolsPagesAndOutputBound(t *testing.T) {
	a, p, _ := toolFixture(t)
	path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"
	first := toolPage(toolObject("one", "POST"))
	first["metadata"].(map[string]any)["continue"] = "next /+"
	first["metadata"].(map[string]any)["resourceVersion"] = "1"
	p[path] = first
	name := strings.Repeat("a", 63) + ".tool"
	last := toolPage(toolObject(name, "GET"))
	last["metadata"].(map[string]any)["resourceVersion"] = "1"
	p[path+"&continue=next+%2F%2B"] = last
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if err != nil || !got.Complete || len(got.Tools) != 9 {
		t.Fatalf("pages %+v %v", got, err)
	}
	many := []map[string]any{}
	for i := 0; i < 129; i++ {
		obj := toolObject(fmt.Sprintf("tool-%03d", i), "GET")
		obj["metadata"].(map[string]any)["uid"] = strings.Repeat("u", 1000) + fmt.Sprint(i)
		many = append(many, obj)
	}
	p[path] = toolPage(many...)
	got, err = a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	raw, _ := json.Marshal(got)
	if err == nil || got.Complete || len(raw) > 60<<10 || len(got.Tools) == 0 {
		t.Fatalf("output bound %d %+v %v", len(raw), got, err)
	}
}
func TestOrkaToolsPartialPageRetainsRows(t *testing.T) {
	a, p, _ := toolFixture(t)
	path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"
	first := toolPage(toolObject("readable", "GET"))
	first["metadata"].(map[string]any)["continue"] = "next"
	p[path] = first
	p[path+"&continue=next"] = func(cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stderr, "Error from server (Forbidden): private-content-canary")
		return fmt.Errorf("fail")
	}
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if err == nil || got.Complete || len(got.Tools) != 8 || got.Tools[7].Name != "readable" {
		t.Fatalf("lost partial row %+v %v", got, err)
	}
}
