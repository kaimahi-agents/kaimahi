package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func createWorkspace(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	instructions := filepath.Join(dir, "input.md")
	writeFile(t, instructions, "Answer briefly.\n\nExact whitespace.\n")
	opt := workspace.CreateOptions{Name: "greeter", Description: "Public questions", Model: "qwen-3.5-2b", InstructionsFile: instructions}
	if err := workspace.Create(context.Background(), root, opt); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(dir, "cache")
}

func extract(t *testing.T, p *workspace.Prepared) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "suite")
	result, err := suiteoras.Pull(context.Background(), p.Path, p.Descriptor.Digest.String(), out)
	if err != nil {
		t.Fatal(err)
	}
	if result.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("pull substituted descriptor")
	}
	return out
}

// Losing exact authored bytes or seeding an invalid case breaks offline creation.
func TestCreateOfflineExactInstructionsAndValidCase(t *testing.T) {
	root, _ := createWorkspace(t)
	if got := string(readFile(t, filepath.Join(root, "agents/greeter/instructions.md"))); got != "Answer briefly.\n\nExact whitespace.\n" {
		t.Fatalf("instructions changed: %q", got)
	}
	if _, err := runtime.ParseEvaluationCase(readFile(t, filepath.Join(root, "eval/example.yaml"))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace.yaml", "agents/greeter/member.yaml", "build-profiles/default.json"} {
		readFile(t, filepath.Join(root, name))
	}
}

// Creation must not merge into an old bundle, second member, or overwrite files.
func TestCreateRefusesExistingArtifacts(t *testing.T) {
	for _, collision := range []string{"workspace.yaml", "agent.yaml", "agents/other/member.yaml", "eval/example.yaml", "build-profiles/default.json"} {
		t.Run(collision, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, collision), "do not replace")
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, "Public instructions")
			err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input})
			if err == nil {
				t.Fatal("accepted existing artifact")
			}
			if got := string(readFile(t, filepath.Join(root, collision))); got != "do not replace" {
				t.Fatalf("overwritten: %q", got)
			}
		})
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct{ name, model, text string }{
		{"", "model", "instructions"}, {"../escape", "model", "instructions"},
		{"with.dots", "model", "instructions"}, {"with_underscore", "model", "instructions"}, {"trailing-", "model", "instructions"},
		{"greeter", "", "instructions"}, {"greeter", " model ", "instructions"},
		{"greeter", "model", "  \n"}, {"greeter", "model", "ghp_" + strings.Repeat("x", 36)},
	} {
		t.Run(test.name+test.model, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, test.text)
			root := filepath.Join(t.TempDir(), "workspace")
			if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: test.name, Model: test.model, InstructionsFile: input}); err == nil {
				t.Fatal("accepted invalid input")
			}
			if _, err := os.Stat(filepath.Join(root, "workspace.yaml")); !os.IsNotExist(err) {
				t.Fatal("published invalid workspace")
			}
		})
	}
}

func TestNativeSourceRefusesTextTheOrkaRendererCannotRepresent(t *testing.T) {
	for _, tc := range []struct{ name, description, instructions, field string }{
		{"NUL instructions", "brief", "Answer briefly.\x00", "instructions"},
		{"CRLF instructions", "brief", "Answer briefly.\r\n", "instructions"},
		{"multiline description", "line one\nline two", "Answer briefly.\n", "description"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "input.md")
			writeFile(t, input, tc.instructions)
			root := filepath.Join(t.TempDir(), "workspace")
			err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Description: tc.description, Model: "test-model", InstructionsFile: input})
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("unrenderable %s should refuse before writing: %v", tc.field, err)
			}
			if _, err := os.Stat(filepath.Join(root, "workspace.yaml")); !os.IsNotExist(err) {
				t.Fatal("invalid native text published source")
			}
		})
	}
	root, cache := createWorkspace(t)
	writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), "Edited\x00instructions")
	if _, err := workspace.Prepare(context.Background(), root, cache); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("edited unrenderable instructions should refuse preparation: %v", err)
	}
}

func TestCreatePreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git/config"), "git state")
	input := filepath.Join(root, "input.md")
	writeFile(t, input, "Public instructions")
	if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(root, ".git/config"))); got != "git state" {
		t.Fatal("Git state changed")
	}
}
