package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAgentBundleKagentFlagsReachExplicitRoute(t *testing.T) {
	for _, args := range [][]string{
		{"bundle", "lift", "missing-bundle", "--runtime", "kagent", "--runtime-version", "v0.10.3", "--bindings", "missing-bindings"},
		{"bundle", "status", "missing-bundle", "--runtime", "kagent", "--runtime-version", "v0.10.3", "--bindings", "missing-bindings"},
	} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "explicit --to-context") {
			t.Fatalf("%s flags did not reach qualified route: %v", args[1], err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s emitted stdout before selection validation: %s", args[1], &out)
		}
	}
}

func TestAgentStatusKagentSelectionFlags(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	cmd, _, err := root.Find([]string{"bundle", "status"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "runtime-version", "to-context", "to-namespace", "bindings", "output"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
}
