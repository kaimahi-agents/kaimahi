package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuitePolicyCommandEmitsCanonicalPayloadAndDigest(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	adminPath := filepath.Join(directory, "admin.json")
	if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminPath, []byte(`{"denyWritePaths":["/etc/secrets"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newSuitePolicyCommand()
	command.SetOut(&output)
	command.SetArgs([]string{"--administrative-policy", adminPath, policyPath})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want 2: %q", len(lines), output.String())
	}
	wantPolicy := `{"denyWritePaths":["/etc/secrets","/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"],"version":"agentsuite.policy/v1"}`
	if lines[0] != wantPolicy {
		t.Fatalf("policy = %q, want %q", lines[0], wantPolicy)
	}
	if !strings.HasPrefix(lines[1], "sha256:") || len(lines[1]) != len("sha256:")+64 {
		t.Fatalf("digest = %q", lines[1])
	}
}

func TestSuitePolicyCommandRejectsInvalidCapabilityContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newSuitePolicyCommand()
	command.SetArgs([]string{path})
	if err := command.Execute(); err == nil {
		t.Fatal("accepted empty mandatory capability set")
	}
}

func TestReadPolicyFileRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxPolicyFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPolicyFile(path); err == nil {
		t.Fatal("accepted oversized policy file")
	}
}
