package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReleaseLiftHelpAndCompletionAreInert(t *testing.T) {
	for _, args := range [][]string{{"lift", "--help"}, {"help", "lift"}, {"__complete", "lift", ""}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatal("help loaded configuration")
		}
		if args[0] != "__complete" && !strings.Contains(out.String(), "--evidence") {
			t.Fatal("missing suite lift contract")
		}
	}
}

func TestReleaseLiftRejectsBeforeConfiguration(t *testing.T) {
	ref := "repo/suite@sha256:" + strings.Repeat("a", 64)
	base := []string{"lift", ref, "--to-context", "target", "--inference", "provider:inference", "--evidence", "private.json"}
	for _, args := range [][]string{
		{"lift"}, {"lift", "down"}, {"lift", "repo:tag"},
		append(append([]string(nil), base...), "--runtime", "kagent"),
		append(append([]string(nil), base...), "--model", "other"),
		append(append([]string(nil), base...), "--override-gate", "why"),
		append(append([]string(nil), base...), "--workspace", "."),
		append(append([]string(nil), base...), "--output", "yaml"),
		append(append([]string(nil), base...), "--from-layout", ".", "--plain-http"),
		append(append([]string(nil), base...), "--context", "other"),
		append(append([]string(nil), base...), "--to-context", ""),
		append(append([]string(nil), base...), "--evidence", ""),
		append(append([]string(nil), base...), "--inference", "copilot"),
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err == nil || *loads != 0 {
			t.Fatalf("%v: err=%v loads=%d", args, err, *loads)
		}
	}
}
