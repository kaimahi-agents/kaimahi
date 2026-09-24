package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func commandUnderTest(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// DESIGN.md §4 adds --runtime to the read commands with an empty
// auto-detection default. The default must stay empty: "auto" is a selection
// policy, not a runtime ID.
func TestReadCommandsExposeRuntimeSelection(t *testing.T) {
	for _, path := range [][]string{{"agent", "list"}, {"status"}} {
		cmd := commandUnderTest(t, path...)
		flag := cmd.Flags().Lookup("runtime")
		if flag == nil {
			t.Fatalf("%v has no --runtime flag", path)
		}
		if flag.DefValue != "" {
			t.Errorf("%v --runtime defaults to %q, not detection", path, flag.DefValue)
		}
		if err := cmd.ParseFlags([]string{"--runtime", "kagent"}); err != nil {
			t.Fatal(err)
		}
		if value, _ := cmd.Flags().GetString("runtime"); value != "kagent" {
			t.Errorf("%v --runtime = %q", path, value)
		}
	}
}

// `kmx status` gains the explicit selectors that populate the singular
// runtime-qualified AgentRef lifecycle Status accepts.
func TestStatusExposesRuntimeSelectors(t *testing.T) {
	cmd := commandUnderTest(t, "status")
	for _, name := range []string{"namespace", "agent"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("status has no --%s selector", name)
		}
	}
}

// The intentional change of the bare-list and bare-status defaults is stated
// where an operator reads it, including that legacy kagent stays explicit.
func TestReadCommandHelpDocumentsDetection(t *testing.T) {
	for _, path := range [][]string{{"agent", "list", "--help"}, {"status", "--help"}} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		if err := execute(path, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--runtime", "detect", "kagent"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v help lacks %q:\n%s", path, want, out.String())
			}
		}
	}
}

// Conflicts fail rather than being ignored, and they fail before the command
// contacts anything: legacy kagent reads its own fixed namespace.
func TestReadCommandsRefuseLegacyNamespaceConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "list", "--runtime", "kagent", "--namespace", "team-a"},
		{"status", "--runtime", "kagent", "--namespace", "team-a"},
	} {
		t.Setenv("PATH", t.TempDir())
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "kagent") {
			t.Fatalf("%v: err = %v", args, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%v printed %q", args, out.String())
		}
	}
}
