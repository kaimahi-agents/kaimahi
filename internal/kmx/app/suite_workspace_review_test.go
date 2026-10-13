package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Review must not turn validation into execution or change authored files.
func TestSuiteWorkspaceReviewReadOnlyCompleteAndDrift(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	before := map[string][]byte{}
	err := filepath.WalkDir(opt.Workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		before[path], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A zero App has neither cluster configuration nor an execution runner.
	review, err := (&App{}).reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if review.root != opt.Workspace || review.suite.Model != "packaged-model" || string(review.suite.Instructions) != "Answer privately" || len(review.inventory) != 2 || review.inventory[0].Case.ID != "first" || review.inventory[1].Case.ID != "second" || review.suite.InstructionsDigest == "" || review.suite.Digest == "" {
		t.Fatalf("incomplete review: %+v", review)
	}
	if err := review.checkCurrent(t.Context()); err != nil {
		t.Fatal(err)
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("source changed: %s: %v", path, err)
		}
	}
	count := 0
	if err := filepath.WalkDir(opt.Workspace, func(_ string, e os.DirEntry, err error) error {
		if !e.IsDir() {
			count++
		}
		return err
	}); err != nil || count != len(before) {
		t.Fatal("review added source files", err)
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/second.yaml"), "id: second\ninput: changed\nexpectContains: [changed]\n")
	if err := review.checkCurrent(t.Context()); err == nil || !strings.Contains(err.Error(), "workspace changed since preparation") {
		t.Fatalf("drift accepted: %v", err)
	}
}

func TestSuiteWorkspaceReviewRefusesBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, string)
	}{
		{"explicit root", "explicit", func(t *testing.T, root string) {}},
		{"unsupported evaluator", "unsupported native suite evaluator", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: say hi\nassertions: [{id: tool, type: toolCalled, tool: inspect}]\n")
		}},
		{"duplicate ID", "duplicate evaluation case ID", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "eval/second.yaml"), "id: first\ninput: say hi\nexpectContains: [hi]\n")
		}},
		{"missing instructions", "instructions", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "agents/greeter/instructions.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", "regular YAML files", func(t *testing.T, root string) {
			p := filepath.Join(root, "eval/second.yaml")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("example.yaml", p); err != nil {
				t.Fatal(err)
			}
		}},
		{"kagent", "Orka", func(t *testing.T, root string) {
			p := filepath.Join(root, "agents/greeter/member.yaml")
			writeFile(t, p, "id: greeter\ninstructions: instructions.md\nmodel: {protocol: openai-compatible, model: packaged-model}\nruntime: {id: kagent, version: v0.10.3, engine: go}\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			opt := nativeEvaluationOptions(t)
			tc.change(t, opt.Workspace)
			root := opt.Workspace
			if tc.name == "explicit root" {
				t.Chdir(root)
				root = ""
			}
			r, err := (&App{}).reviewSuiteWorkspace(t.Context(), root)
			if r != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("review accepted or wrong refusal: %v", err)
			}
		})
	}
	t.Run("missing exact root no ancestor fallback", func(t *testing.T) {
		opt := nativeEvaluationOptions(t)
		nested := filepath.Join(opt.Workspace, "nested")
		if err := os.Mkdir(nested, 0700); err != nil {
			t.Fatal(err)
		}
		r, err := (&App{}).reviewSuiteWorkspace(t.Context(), nested)
		if r != nil || err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("fell back: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r, err := (&App{}).reviewSuiteWorkspace(ctx, "/missing")
		if r != nil || err != context.Canceled {
			t.Fatalf("cancelled review: %v", err)
		}
	})
}

func TestSuiteWorkspaceReviewKeepsAllAssertionDefinitions(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	opt := nativeEvaluationOptions(t)
	writeFile(t, filepath.Join(opt.Workspace, "eval/second.yaml"), "id: second\ninput: public content\nassertions:\n- {id: positive, type: contains, value: yes}\n- {id: negative, type: notContains, value: secret}\n- {id: format, type: regex, pattern: '^yes$'}\n")
	r, err := (&App{}).reviewSuiteWorkspace(t.Context(), opt.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(r.caseLines(1, 72), "\n")
	for _, want := range []string{"second", "public content", "positive", "contains", "yes", "negative", "notContains", "secret", "format", "regex", "^yes$"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("missing %q in %s", want, lines)
		}
	}
	if r.suite.Runtime != (workspace.RuntimeProfile{ID: "orka"}) {
		t.Fatal("wrong runtime")
	}
}
