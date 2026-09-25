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

// The legacy kagent runtime is gone, and so are the commands that only ever
// drove it. Each must fail locally — before configuration is loaded and long
// before a cluster is reached — rather than arriving at a runtime that is no
// longer installed.
func TestLegacyRuntimeCommandsAreRetired(t *testing.T) {
	for _, args := range [][]string{
		{"govern"}, {"govern", "hello-world"}, {"--context", "kind-stale", "govern"},
		{"use"}, {"use", "ollama"},
		{"agent", "edit", "hello-world"},
	} {
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

// `agent chat` keeps its name and loses the legacy transport. The flags below
// were kagent's alone: a resumable server-side session and the raw A2A task
// its one-shot invoke printed. Neither has a meaning against Orka, and a flag
// that parses and does nothing is worse than one that does not exist.
func TestLegacyChatTransportFlagsAreRetired(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find([]string{"agent", "chat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session", "json"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("agent chat still carries the legacy --%s flag", name)
		}
	}
	if err := cmd.ParseFlags([]string{"--session", "abc"}); err == nil {
		t.Error("agent chat still parses --session")
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
