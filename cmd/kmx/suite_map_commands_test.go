package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuiteMapIsOfflineAndYAMLReport(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	err := execute([]string{"package", "map", "../../.github/agents/hello.agent.md", "--workspace", filepath.Join(t.TempDir(), "mapped"), "--model", "qwen2.5:3b", "-o", "yaml"}, deps)
	if err != nil || *loads != 0 || !strings.Contains(out.String(), "requiresNativeEvaluation: true") {
		t.Fatalf("err=%v loads=%d out=%s", err, *loads, &out)
	}
}

func TestMarkdownDeployAndLiftPlanComposeOffline(t *testing.T) {
	for _, verb := range []string{"deploy", "lift"} {
		t.Run(verb, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			t.Setenv("PATH", t.TempDir())
			dir := t.TempDir()
			config := "version: 1\npublishRepository: registry.example/hello\ntargets:\n  eval:\n    context: kind-eval\n    namespace: orka-system\n    runtime: orka\n    inference: provider:local\n  prod:\n    context: remote-prod\n    namespace: orka-system\n    runtime: orka\n    inference: provider:remote\n    evaluationTarget: eval\n"
			if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			args := []string{verb, "../../.github/agents/hello.agent.md", "--to", "prod", "--directory", dir, "--model", "qwen2.5:3b", "--plan", "-o", "json"}
			if verb == "deploy" {
				args = append([]string{"package"}, args...)
			}
			err := execute(args, deps)
			if err != nil || *loads != 0 {
				t.Fatalf("err=%v loads=%d out=%s diag=%s", err, *loads, &out, &diag)
			}
			if !strings.Contains(out.String(), `"caseRuns":2`) || !strings.Contains(diag.String(), "Mapped md hello") || !strings.Contains(diag.String(), "qwen2.5:3b") {
				t.Fatalf("out=%s diag=%s", &out, &diag)
			}
			if _, err := os.Stat(filepath.Join(dir, "workspace.yaml")); !os.IsNotExist(err) {
				t.Fatal("mapping wrote workspace into config/source directory")
			}
		})
	}
}
