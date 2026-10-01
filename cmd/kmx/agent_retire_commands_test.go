package main

import "testing"

func TestAgentRetireCommandFlags(t *testing.T) {
	cmd, _, err := newRootCommand(&commandState{deps: productionDependencies()}).Find([]string{"agent", "retire"})
	if err != nil || cmd == nil || cmd.Name() != "retire" {
		t.Fatalf("retire command missing: %v", err)
	}
	for _, name := range []string{"to-context", "to-namespace", "plan", "delete-adopted"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
}
