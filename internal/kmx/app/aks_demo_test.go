package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func aksDemoBlocks(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "aks-demo.md"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	var current []string
	inBash := false
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "```bash":
			inBash = true
			current = nil
		case inBash && line == "```":
			blocks = append(blocks, strings.Join(current, "\n")+"\n")
			inBash = false
		case inBash:
			current = append(current, line)
		}
	}
	if inBash || len(blocks) == 0 {
		t.Fatal("AKS demo has malformed or no Bash blocks")
	}
	return blocks
}

func aksDemoBlock(t *testing.T, marker string) string {
	t.Helper()
	for _, block := range aksDemoBlocks(t) {
		if strings.Contains(block, marker) {
			return block
		}
	}
	t.Fatalf("AKS demo block containing %q not found", marker)
	return ""
}

func aksDemoShells(t *testing.T) []string {
	t.Helper()
	shells := []string{"bash"}
	if _, err := exec.LookPath("zsh"); err == nil {
		shells = append(shells, "zsh")
	} else {
		t.Log("zsh is not installed; cross-shell behavior runs under Bash only on this host")
	}
	return shells
}

func TestAKSDemoAllShellBlocksParseAndLint(t *testing.T) {
	blocks := aksDemoBlocks(t)
	for i, block := range blocks {
		t.Run("bash/block-"+fmt.Sprint(i+1), func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(block)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Bash syntax: %v\n%s", err, out)
			}
		})
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		for i, block := range blocks {
			t.Run("zsh/block-"+fmt.Sprint(i+1), func(t *testing.T) {
				cmd := exec.Command(zsh, "-n")
				cmd.Stdin = strings.NewReader(block)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Zsh syntax: %v\n%s", err, out)
				}
			})
		}
	} else {
		t.Log("zsh is not installed; skipping optional Zsh syntax pass")
	}
	if shellcheck, err := exec.LookPath("shellcheck"); err == nil {
		cmd := exec.Command(shellcheck, "-s", "bash", "-")
		cmd.Stdin = strings.NewReader(strings.Join(blocks, "\n"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ShellCheck: %v\n%s", err, out)
		}
	} else {
		t.Log("shellcheck is not installed; skipping optional lint pass")
	}
}

func runAKSDemoBlock(t *testing.T, shell, block string, env map[string]string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(shell, "-c", block)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string(nil), os.Environ()...)
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return cmd.CombinedOutput()
}

func fakeAKSDemoKMX(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kmx")
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KMX_DEMO_TEST_CALLS\"\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, calls
}

func TestAKSDemoRejectsMultilineProviderInputBeforeSetup(t *testing.T) {
	block := aksDemoBlock(t, "set_demo_variables()")
	kmx, calls := fakeAKSDemoKMX(t, "exit 0")
	for _, shell := range aksDemoShells(t) {
		t.Run(shell, func(t *testing.T) {
			_, err := runAKSDemoBlock(t, shell, block, map[string]string{
				"KMX": kmx, "KMX_DEMO_TEST_CALLS": calls, "ACR_NAME": "registry123",
				"MODEL_ID": "model", "MODEL_BASE_URL": "https://models.example.invalid/v1\nspec:\n  type: anthropic",
				"AGENT": "greeter", "INFERENCE_PROVIDER": "inference", "MODEL_SECRET": "model-key",
				"ORKA_" + "API_KEY": "present",
			})
			if err == nil {
				t.Fatal("multiline endpoint passed demo validation")
			}
			if raw, readErr := os.ReadFile(calls); readErr == nil && len(raw) != 0 {
				t.Fatalf("validation reached KMX setup: %s", raw)
			}
		})
	}
}

func TestAKSDemoRequiresCoherentProofBeforeAzureCreation(t *testing.T) {
	coherence := aksDemoBlock(t, "check_evidence_coherence()")
	provision := aksDemoBlock(t, "provision_demo_aks()")
	kmx, calls := fakeAKSDemoKMX(t, `case "$*" in
  *"push "*) exit 23 ;;
  *) exit 0 ;;
esac`)
	for _, shell := range aksDemoShells(t) {
		t.Run(shell, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			env := map[string]string{
				"KMX": kmx, "KMX_DEMO_TEST_CALLS": calls, "PRIVATE_DIR": root,
				"WORKSPACE": filepath.Join(root, "workspace"), "EVIDENCE": filepath.Join(root, "passing.json"),
				"AGENT": "greeter", "RESOURCE_GROUP": "demo-rg", "ACR_NAME": "registry123",
				"AKS_CLUSTER": "demo-aks", "AZURE_LOCATION": "westus3",
			}
			if _, err := runAKSDemoBlock(t, shell, coherence, env); err == nil {
				t.Fatal("failed exact-package coherence check returned success")
			}
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := runAKSDemoBlock(t, shell, provision, env); err == nil {
				t.Fatal("Azure provisioning ran without the same-shell proof marker")
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != 0 {
				t.Fatalf("proof gate contacted KMX/Azure: %s", raw)
			}
		})
	}
}

func TestAKSDemoInvalidatesEarlierProofWhenFreshCoherenceFails(t *testing.T) {
	coherence := aksDemoBlock(t, "check_evidence_coherence()")
	provision := aksDemoBlock(t, "provision_demo_aks()")
	kmx, calls := fakeAKSDemoKMX(t, `case "$*" in
  *"eval "*) : > "$EVIDENCE"; exit 0 ;;
  *"push "*) [ ! -e "$KMX_DEMO_TEST_FAIL_PUSH" ] ;;
  *"aks up"*) exit 0 ;;
  *) exit 99 ;;
esac`)
	for _, shell := range aksDemoShells(t) {
		t.Run(shell, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			failPush := filepath.Join(root, "fail-push")
			script := coherence + "\n: > \"$KMX_DEMO_TEST_FAIL_PUSH\"\n" + provision + `
rc=$?
[ -z "${KMX_DEMO_PROOF_READY+x}" ] || exit 91
exit "$rc"
`
			_, err := runAKSDemoBlock(t, shell, script, map[string]string{
				"KMX": kmx, "KMX_DEMO_TEST_CALLS": calls, "KMX_DEMO_TEST_FAIL_PUSH": failPush,
				"PRIVATE_DIR": root, "WORKSPACE": filepath.Join(root, "workspace"),
				"EVIDENCE": filepath.Join(root, "passing.json"), "AGENT": "greeter", "LOCAL_CONTEXT": "kind-local", "INFERENCE_PROVIDER": "inference",
				"RESOURCE_GROUP": "demo-rg", "ACR_NAME": "registry123",
				"AKS_CLUSTER": "demo-aks", "AZURE_LOCATION": "westus3",
			})
			if err == nil {
				t.Fatal("failed fresh coherence check retained earlier authorization")
			}
			raw, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(raw), "push ") != 2 || strings.Contains(string(raw), "aks up") {
				t.Fatalf("fresh proof gate did not stop Azure creation: %s", raw)
			}
		})
	}
}

func TestAKSDemoTeardownKeepsCloudFailureAndStillCleansLocal(t *testing.T) {
	block := aksDemoBlock(t, "teardown_demo()")
	kmx, calls := fakeAKSDemoKMX(t, `case "$*" in
  *"aks down"*) exit 17 ;;
  *"local down"*) exit 0 ;;
  *) exit 99 ;;
esac`)
	for _, shell := range aksDemoShells(t) {
		t.Run(shell, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := runAKSDemoBlock(t, shell, block, map[string]string{
				"KMX": kmx, "KMX_DEMO_TEST_CALLS": calls, "RESOURCE_GROUP": "demo-rg",
				"AKS_CLUSTER": "demo-aks", "LOCAL_CLUSTER": "demo-local", "LOCAL_CONTEXT": "kind-demo-local",
			})
			if err == nil {
				t.Fatal("failed cloud teardown returned success")
			}
			raw, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, want := range []string{"aks down", "local down"} {
				if !strings.Contains(string(raw), want) {
					t.Fatalf("teardown did not attempt %s: %s", want, raw)
				}
			}
		})
	}
}
