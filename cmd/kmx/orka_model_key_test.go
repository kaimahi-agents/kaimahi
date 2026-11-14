package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOrkaModelKeyArguments(t *testing.T) {
	for _, tc := range []struct {
		route, vendor string
		valid         bool
	}{
		{"key", "openai", true}, {"key", "anthropic", true}, {"copilot", "", true},
		{"key", "", false}, {"key", "azure-openai", false}, {"copilot", "openai", false}, {"other", "openai", false},
	} {
		t.Run(tc.route+"/"+tc.vendor, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			cmd := newOrkaModelLeaf(&commandState{deps: deps}, "connect")
			args := []string{"--namespace", "orka-system", "--provider", "workspace", "--route", tc.route, "--model", "gpt-4.1", "--credential-generation", "22222222-2222-4222-8222-222222222222", "-ojsonl"}
			if tc.vendor != "" {
				args = append(args, "--vendor", tc.vendor)
			}
			err := cmd.ParseFlags(args)
			if err == nil {
				err = cmd.Args(cmd, cmd.Flags().Args())
			}
			if (err == nil) != tc.valid || *loads != 0 {
				t.Fatalf("valid=%v effects=%d: %v", tc.valid, *loads, err)
			}
		})
	}
	// An opaque flag operand that looks like output must not select JSONL.
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	_ = execute([]string{"cluster", "orka", "model", "connect", "--vendor", "-ojsonl"}, deps)
	if strings.Contains(out.String(), `"schemaVersion"`) {
		t.Fatal("vendor operand selected machine output")
	}
}
