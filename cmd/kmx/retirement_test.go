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

// The former edit stub is removed with the agent parent, including its old flags
// and help path. It must not load configuration or print a migration stub.
func TestLegacyAgentEditIsRemovedBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "edit", "hello-world"},
		{"agent", "edit", "hello-world", "--file", "agent.yaml"},
		{"agent", "edit", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "unknown command \"agent\"") || *loads != 0 || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("removed edit: err=%v loads=%d stdout=%q stderr=%q", err, *loads, &out, &errOut)
			}
		})
	}
}

func TestLegacyChatTransportFlagsAreRetired(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"chat", "hello-world", "--session", "abc"}, "--session"},
		{[]string{"chat", "hello-world", "--json"}, "--json"},
		{[]string{"chat", "--session", "abc"}, "--session"},
		{[]string{"chat", "--json"}, "--json"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "kmx chat --namespace") {
				t.Fatalf("retired flag must name the chat replacement: %v: %v", tc.args, err)
			}
			if *loads != 0 {
				t.Fatalf("retired flag loaded operational configuration: %v", tc.args)
			}
		})
	}
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	err := execute([]string{"chat", "hello-world", "--json"}, deps)
	if err == nil || !strings.Contains(err.Error(), "no raw A2A JSON equivalent for an existing Agent") || strings.Contains(err.Error(), "create --task") {
		t.Fatalf("retired --json must describe the existing-Agent limitation, not suggest creating another: %v", err)
	}
	if *loads != 0 {
		t.Fatal("retired --json loaded operational configuration")
	}
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find([]string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session", "json"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || !flag.Hidden {
			t.Errorf("retired --%s must parse but stay out of help", name)
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
