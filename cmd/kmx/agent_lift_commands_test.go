package main

import (
	"strings"
	"testing"
)

// Removing the lift registration or accepting installation flags would break
// the non-interactive bundle workflow's public command boundary.
func TestAgentLiftCommandSurface(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	cmd, _, err := root.Find([]string{"agent", "lift"})
	if err != nil || cmd == nil || cmd.Name() != "lift" {
		t.Fatalf("agent lift command missing: %v", err)
	}
	for _, name := range []string{"to-context", "to-namespace", "inference", "plan"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	for _, name := range []string{"install-orka", "install-k8s-tool", "subscription", "cluster"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("unexpected --%s", name)
		}
	}
	if err := cmd.Args(cmd, nil); err == nil || !strings.Contains(err.Error(), "bundle") {
		t.Fatalf("missing bundle not refused: %v", err)
	}
}
