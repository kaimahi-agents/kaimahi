package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCredentialMountArguments(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "up", "--step", "cluster", "--credential-mount="},
		{"cluster", "up", "--step", "cluster", "--credential-mount", "arbitrary"},
		{"cluster", "up", "--credential-mount", "copilot-v1"},
		{"cluster", "up", "--step", "model", "--credential-mount", "copilot-v1"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if err == nil || *loads != 0 || !strings.Contains(err.Error(), "credential-mount requires") {
			t.Fatalf("args%q err%v config%d", args, err, *loads)
		}
	}
}
func TestCredentialMountOperandIsNotJSONOutput(t *testing.T) {
	if got := provisioningJSONInvocation([]string{"cluster", "up", "--credential-mount", "-ojsonl"}); got != "" {
		t.Fatal("mount operand selected JSONL")
	}
}
