package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestApprovalCommandsAreRetiredBeforeConfiguration(t *testing.T) {
	for _, verb := range []string{"request", "approve", "deny", "approvals", "grants", "audit"} {
		for _, args := range [][]string{{verb}, {verb, "budget"}, {"--context", "kind-stale", verb}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				var out, errOut bytes.Buffer
				deps, loads := testDependencies(&out, &errOut)
				err := execute(args, deps)
				if err == nil || !strings.Contains(err.Error(), "unknown command") {
					t.Fatalf("retired command must be unknown: %v: %v", args, err)
				}
				if *loads != 0 {
					t.Fatalf("retired command loaded operational configuration: %v", args)
				}
			})
		}
	}
}

// A stale command must fail locally, not reach config or a cluster.
func TestGatewayCommandsAreRetired(t *testing.T) {
	for _, args := range [][]string{{"tools"}, {"workflow"}, {"credential", "capture", "github", "owner/repo"}, {"audit", "tool"}, {"request", "tool", "delete"}, {"request", "budget", "tokens", "--args", "{}"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(args, deps)
			if err == nil {
				t.Fatalf("retired invocation accepted: %v", args)
			}
			if *loads != 0 {
				t.Fatalf("retired invocation loaded operational configuration: %v: %v", args, err)
			}
		})
	}
}
