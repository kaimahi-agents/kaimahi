package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestDiscoveryNamespaceMissingNeedsExactDiagnostic(t *testing.T) {
	for _, tc := range []struct{ diagnostic, state string }{{"Error from server (NotFound): NotFound", "unknown"}, {`Error from server (NotFound): namespaces "other" not found`, "unknown"}, {`Error from server (NotFound): namespaces "orka-system" not found`, "missing"}} {
		t.Run(tc.state+tc.diagnostic, func(t *testing.T) {
			a, p := targetFixture(t)
			p["/api/v1/namespaces/orka-system"] = func(c *exec.Cmd) error { fmt.Fprint(c.Stderr, tc.diagnostic); return fmt.Errorf("exit1") }
			p["/api/v1"] = map[string]any{"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": "v1", "resources": []any{map[string]any{"name": "namespaces", "kind": "Namespace", "namespaced": false, "verbs": []string{"get"}}}}
			r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "kind-test", Namespace: "orka-system"})
			if e == nil || r.Complete {
				t.Fatal(r)
			}
			for _, c := range r.Checks {
				if c.Scope == "namespace" && c.State != tc.state {
					t.Fatal(r.Checks)
				}
			}
		})
	}
}
func TestDiscoveryRejectedTargetSelectorNotRendered(t *testing.T) {
	for _, ns := range []string{"bad\x1b[31m", strings.Repeat("a", 10000), "sk-" + strings.Repeat("x", 40)} {
		a := captureTestApp()
		a.captureCommand = func(_ctx context.Context, c *exec.Cmd) error { t.Fatal("invalid dispatched"); return nil }
		r, e := a.Discover(t.Context(), DiscoveryOptions{Context: "chosen", Namespace: ns})
		b, _ := json.Marshal(r)
		if e == nil || r.Target != nil || strings.Contains(string(b), ns) {
			t.Fatal("rejected selector leaked", len(b), e)
		}
	}
}
