package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleHelpAndNonterminal(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"cluster", "console", "--help"}, deps); err != nil {
		t.Fatal(err)
	}

	if *loads != 0 {
		t.Fatal("help loaded operational configuration")
	}
	for _, flag := range []string{"--demo", "--local-context", "--remote-context", "--namespace"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s", flag)
		}
	}
	for _, want := range []string{"R opens recent read-only Orka runs", "list/get Tasks", "get the kube-system Namespace"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help omits %q", want)
		}
	}
	if err := execute([]string{"cluster", "console", "--demo"}, deps); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err=%v", err)
	}
}

func TestConsoleRefusesInvalidEnforcementSelectionBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "console", "--enforcement-file", ""},
		{"cluster", "console", "--enforcement-file", "   "},
		{"cluster", "console", "--demo", "--enforcement-file", "requirement.json"},
	} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err == nil || !strings.Contains(err.Error(), "enforcement-file") {
				t.Fatalf("invalid console enforcement selection did not reach its guard: %v", err)
			}
			if *loads != 0 {
				t.Fatal("invalid selection loaded operational configuration")
			}
		})
	}
}
