package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMDReviewRejectsUnsupportedFlagsBeforeEffects(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	file := filepath.Join(t.TempDir(), "agent.agent.md")
	if err := os.WriteFile(file, []byte("---\nname: agent\ndescription: fixture\ntools: []\n---\nSay hello.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", file, "--type", "md", "--provider-url", "https://example.invalid"},
		{"test", file, "--type", "md", "--to-context", "prod"},
		{"test", file, "--type", "md", "--out", "pass.json"},
		{"test", file, "--type", "md", "--bindings", "anything"},
		{"test", file, "--type", "md", "--case-timeout", "1s"},
		{"run", file, "--type", "md", "--tool-bindings", "anything"},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(args, deps)
		if err == nil || (!strings.Contains(err.Error(), "not supported for md") && !strings.Contains(err.Error(), "unknown flag")) || *loads != 0 || out.Len() != 0 {
			t.Fatalf("%v: %v loads=%d out=%q", args, err, *loads, out.String())
		}
	}
}

func TestMDReviewYAMLAgentNamedHelp(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	for _, name := range []string{"help"} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		err := execute([]string{"create", name, "--type", "md", "--directory", t.TempDir(), "-o", "yaml"}, deps)
		if err != nil || !strings.Contains(out.String(), "created: true") || strings.HasPrefix(out.String(), "{") {
			t.Fatalf("%s: %s %v", name, &out, err)
		}
	}
}
