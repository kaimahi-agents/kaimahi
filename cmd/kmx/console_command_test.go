package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleHelpAndNonterminal(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"console", "--help"}, deps); err != nil {
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
	if !strings.Contains(out.String(), "R opens recent read-only Orka runs") {
		t.Fatal("help omits the read-only run shortcut")
	}
	if err := execute([]string{"console", "--demo"}, deps); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err=%v", err)
	}
}
