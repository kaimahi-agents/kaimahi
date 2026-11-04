package app

import (
	"fmt"
	"os/exec"
	"testing"
)

func TestOrkaToolsRequirementsDoNotListTools(t *testing.T) {
	a, p, _ := toolFixture(t)
	lists, exact := 0, 0
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = func(cmd *exec.Cmd) error {
		lists++
		fmt.Fprint(cmd.Stderr, "Error from server (Forbidden): tools may not be listed")
		return fmt.Errorf("exit1")
	}
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools/inventory"] = func(cmd *exec.Cmd) error {
		exact++
		fmt.Fprint(cmd.Stdout, `{"apiVersion":"core.orka.ai/v1alpha1","kind":"Tool","metadata":{"name":"inventory","namespace":"orka-system","uid":"uid-inventory","resourceVersion":"1","generation":1},"spec":{"http":{"url":"https://example.invalid","method":"POST"}},"status":{"available":true}}`)
		return nil
	}
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: []OrkaToolRequirement{{Kind: "http", Name: "inventory", Enabled: true}, {Kind: "builtin", Name: "web_fetch", Enabled: true}, {Kind: "http", Name: "disabled", Enabled: false}}})
	if err != nil || !got.Complete || lists != 0 || exact != 1 || len(got.Tools) != 3 || got.Tools[0].Availability != "available" || got.Tools[1].Availability != "available" || got.Tools[2].Availability != "disabled" {
		t.Fatalf("explicit requirements require listing: %+v err=%v lists=%d exact=%d", got, err, lists, exact)
	}
}
func TestOrkaToolsBuiltinRequirementIgnoresUnrelatedInventory(t *testing.T) {
	a, p, _ := toolFixture(t)
	many := []map[string]any{}
	for i := 0; i < 129; i++ {
		many = append(many, toolObject(fmt.Sprintf("unrelated-%d", i), "GET"))
	}
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = toolPage(many...)
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: []OrkaToolRequirement{{Kind: "builtin", Name: "web_fetch", Enabled: true}}})
	if err != nil || !got.Complete || len(got.Tools) != 1 || got.Tools[0].Availability != "available" {
		t.Fatalf("unrelated inventory blocks builtin: %+v %v", got, err)
	}
}
