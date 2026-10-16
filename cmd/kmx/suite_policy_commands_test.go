package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
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

func TestSuitePolicyCommandNetworkV2(t *testing.T) {
	const endpoint = `{"address":"10.20.30.40","protocol":"TCP","port":443}`
	const other = `{"address":"10.20.30.41","protocol":"TCP","port":80}`
	for _, tc := range []struct {
		name, admin, want string
		refused           bool
	}{
		{"omitted", "", `{"network":{"egressAllow":[{"address":"10.20.30.40","port":443,"protocol":"TCP"},{"address":"10.20.30.41","port":80,"protocol":"TCP"}]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`, false},
		{"intersection", `{"network":{"egressAllow":[` + endpoint + `]}}`, `{"network":{"egressAllow":[{"address":"10.20.30.40","port":443,"protocol":"TCP"}]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`, false},
		{"deny all", `{"network":{"egressAllow":[]}}`, `{"network":{"egressAllow":[]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`, false},
		{"wrong version admin", `{"denyWritePaths":["/workspace"]}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			policyPath := filepath.Join(dir, "policy.json")
			if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v2","network":{"egressAllow":[`+other+`,`+endpoint+`]},"requiredCapabilities":["network/egress-allow/v1"]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"suite", "policy", policyPath}
			if tc.admin != "" {
				adminPath := filepath.Join(dir, "admin.json")
				if err := os.WriteFile(adminPath, []byte(tc.admin), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--administrative-policy", adminPath)
			}
			var output, diagnostics bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostics)
			err := execute(args, deps)
			if *loads != 0 {
				t.Fatal("policy resolution loaded operational config")
			}
			if tc.refused {
				if err == nil || output.Len() != 0 {
					t.Fatalf("want closed admin refusal, got %v / %q", err, output.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != 2 || lines[0] != tc.want || !strings.HasPrefix(lines[1], "sha256:") {
				t.Fatalf("unexpected payload: %q", output.String())
			}
		})
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

func TestSuitePolicyCommandSanitizesMalformedInputDiagnostics(t *testing.T) {
	const valid = `{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`
	const malformed = `{} "\u001b[2J\u0007\u009b31m"`
	for _, input := range []string{"suite", "administrative"} {
		t.Run(input, func(t *testing.T) {
			directory := t.TempDir()
			policyPath := filepath.Join(directory, "policy.json")
			adminPath := filepath.Join(directory, "admin.json")
			policyJSON, adminJSON := valid, `{"denyWritePaths":["/etc/secrets"]}`
			if input == "suite" {
				policyJSON = malformed
			} else {
				adminJSON = malformed
			}
			for path, data := range map[string]string{policyPath: policyJSON, adminPath: adminJSON} {
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output, diagnostics bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostics)
			err := execute([]string{"suite", "policy", policyPath, "--administrative-policy", adminPath}, deps)
			if *loads != 0 {
				t.Fatalf("offline policy command loaded operational configuration %d times", *loads)
			}
			if err == nil || !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("error = %v, want malformed trailing JSON refusal", err)
			}
			for _, r := range err.Error() {
				if unicode.IsControl(r) {
					t.Fatalf("unsafe diagnostic contains terminal control U+%04X", r)
				}
			}
			if strings.ContainsAny(diagnostics.String(), "\x1b\x07\u009b") {
				t.Fatalf("unsafe stderr: %q", diagnostics.String())
			}
			var diagnostic *suiteDiagnosticError
			if !errors.As(err, &diagnostic) || errors.Unwrap(diagnostic) == nil {
				t.Fatalf("error = %T, want suite diagnostic retaining its cause", err)
			}
			if output.Len() != 0 {
				t.Fatalf("refusal emitted stdout: %q", output.String())
			}
		})
	}
}

func TestSuitePolicyCommandRefusesExplicitEmptyAdministrativePolicy(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		flags   []string
		wantErr bool
	}{
		{name: "omitted remains optional"},
		{name: "empty equals", flags: []string{"--administrative-policy="}, wantErr: true},
		{name: "empty argument", flags: []string{"--administrative-policy", ""}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostics)
			args := append([]string{"suite", "policy", policyPath}, tc.flags...)
			err := execute(args, deps)
			if *loads != 0 {
				t.Fatalf("offline policy command loaded operational configuration %d times", *loads)
			}
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "--administrative-policy cannot be empty") {
					t.Fatalf("error = %v, want explicit empty administrative-policy refusal", err)
				}
				if output.Len() != 0 {
					t.Fatalf("refusal emitted stdout: %q", output.String())
				}
			} else if err != nil || !strings.Contains(output.String(), "sha256:") {
				t.Fatalf("omitted administrative policy: error = %v, output = %q", err, output.String())
			}
		})
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
