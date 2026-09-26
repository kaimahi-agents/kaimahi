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

// Retired invocations fail locally with a migration path, before configuration
// loading or cluster access. Hidden stubs remain parseable for old scripts.
func TestLegacyRuntimeCommandsAreRetired(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"govern"}, "kmx migrate"},
		{[]string{"govern", "hello-world"}, "kmx migrate"},
		{[]string{"govern", "hello-world", "--model", "governed-ollama"}, "kmx migrate"},
		{[]string{"--context", "kind-stale", "govern"}, "kmx migrate"},
		{[]string{"use"}, "kmx models add"},
		{[]string{"use", "ollama"}, "kmx models add"},
		{[]string{"use", "ollama", "--agent", "hello-world"}, "kmx models add"},
		{[]string{"agent", "edit", "hello-world"}, "kubectl"},
		{[]string{"agent", "edit", "hello-world", "--file", "agent.yaml"}, "kubectl"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("retired command must name replacement %q: %v: %v", tc.want, tc.args, err)
			}
			if *loads != 0 {
				t.Fatalf("retired command loaded operational configuration: %v", tc.args)
			}
		})
	}
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	root := newRootCommand(&commandState{deps: deps})
	for _, path := range [][]string{{"govern"}, {"use"}, {"agent", "edit"}} {
		cmd, _, err := root.Find(path)
		if err != nil || !cmd.Hidden {
			t.Errorf("retired command %v must exist but be hidden: %v", path, err)
		}
	}
}

func TestLegacyChatTransportFlagsAreRetired(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agent", "chat", "hello-world", "--session", "abc"}, "--session"},
		{[]string{"agent", "chat", "hello-world", "--json"}, "--json"},
		{[]string{"agent", "chat", "--session", "abc"}, "--session"},
		{[]string{"agent", "chat", "--json"}, "--json"},
		{[]string{"agent", "chat", "hello-world", "--interactive", "--session", "abc"}, "--session"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "--interactive") {
				t.Fatalf("retired flag must name interactive replacement: %v: %v", tc.args, err)
			}
			if *loads != 0 {
				t.Fatalf("retired flag loaded operational configuration: %v", tc.args)
			}
		})
	}
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find([]string{"agent", "chat"})
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
