package agentsuite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	builder "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

func TestResolveSandboxPlanValidatesLooseContentEvenWithOCILayout(t *testing.T) {
	fixture := filepath.Join("..", "..", "..", "agentsuite", "testdata", "minimal")
	root := filepath.Join(t.TempDir(), "hybrid")
	if _, err := oras.Push(context.Background(), fixture, root, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "agentsuite.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"schemaVersion": "1.0.0-draft"`, `"schemaVersion": "unsupported"`, 1))
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Auto-detection validates the embedded artifact, not the loose manifests.
	if _, err := spec.ValidatePath(root); err != nil {
		t.Fatal(err)
	}
	plan, err := builder.ResolveSandboxPlan(root, builder.BuildSelection{Agent: "not-declared"})
	if plan != nil || err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("plan = %+v, error = %v; want loose-content conformance refusal", plan, err)
	}
}
