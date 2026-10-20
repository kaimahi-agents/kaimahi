package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSuiteFailureContentFlag(t *testing.T) {
	for _, cmd := range []string{"eval", "lift"} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute([]string{cmd, "--show-content", "--help"}, deps); err != nil || *loads != 0 || !strings.Contains(out.String(), "--show-content") || strings.Contains(out.String(), "subset or content output") {
			t.Fatalf("inert flag help: %v loads=%d", err, *loads)
		}
	}
	for _, args := range [][]string{
		{"lift", "--to", "prod", "--plan", "--show-content"},
		{"lift", "repo/suite@sha256:" + strings.Repeat("a", 64), "--show-content"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if err == nil || *loads != 0 || !strings.Contains(err.Error(), "--show-content requires executing workspace evaluation") {
			t.Fatalf("invalid content selection reached config: %v loads=%d", err, *loads)
		}
	}
}
