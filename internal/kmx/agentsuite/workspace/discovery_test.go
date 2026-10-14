package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestDiscoverNearestWorkspaceAndExplicitOverride(t *testing.T) {
	root := t.TempDir()
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, instructions, "Hello.")
	if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "outer", Model: "model-a", InstructionsFile: instructions}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "nested", "deep")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := workspace.Discover(child, "")
	if err != nil || got != root {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := workspace.Discover(child, child); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatal("explicit root fell back", err)
	}
	inner := filepath.Join(root, "nested")
	if err := workspace.Create(context.Background(), inner, workspace.CreateOptions{Name: "inner", Model: "model-b", InstructionsFile: instructions}); err != nil {
		t.Fatal(err)
	}
	got, err = workspace.Discover(child, "")
	if err != nil || got != inner {
		t.Fatal("nearest marker not selected", got, err)
	}
	got, err = workspace.Discover(child, root)
	if err != nil || got != root {
		t.Fatal("override ignored", got, err)
	}
	writeFile(t, filepath.Join(inner, "workspace.yaml"), "invalid: true\n")
	if _, err = workspace.Discover(child, ""); err == nil || errors.Is(err, workspace.ErrNotFound) {
		t.Fatal("malformed nearest marker fell back", err)
	}
}

func TestImageWorkspaceUsesSharedPreparationAndBindsCases(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	if err := os.Chmod(cache, 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "exact instructions\n")
	opt := workspace.CreateOptions{Name: "image", Execution: workspace.ImageExecution, InstructionsFile: input, ContextTokens: 8192, OutputTokens: 1024}
	if err := workspace.Create(context.Background(), root, opt); err != nil {
		t.Fatal(err)
	}
	p, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Load(context.Background(), p); err == nil {
		t.Fatal("image suite accepted by native execution loader")
	}
	writeFile(t, filepath.Join(root, "eval", "example.yaml"), "id: example\ninput: What is 3+3?\nexpectContains: [\"6\"]\n")
	next, err := workspace.Prepare(context.Background(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if p.Descriptor.Digest == next.Descriptor.Digest {
		t.Fatal("case change did not change exact suite identity")
	}
	if err := workspace.CheckCurrent(context.Background(), root, p); err == nil {
		t.Fatal("stale source accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "agentsuite.json")); !os.IsNotExist(err) {
		t.Fatal("generated graph written into source")
	}
	if _, err := os.Stat(filepath.Join(root, ".kmx")); !os.IsNotExist(err) {
		t.Fatal("private state written into source")
	}
}
