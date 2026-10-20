package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Cache migration must remain actionable through redacted offline preflights,
// without deleting historical proof or invalidating proof for an unchanged S.
func TestSuiteCacheMigrationPreflightAndProofRetention(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	root, cache := store.workspace, store.cache
	publicCommitFixture(t, store, suite, raw)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Dir(prepared.Path)
	record := filepath.Join(entry, "prepared.json")
	original, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "packaging")
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{Out: io.Discard, Err: io.Discard}
	out := filepath.Join(t.TempDir(), "evidence.json")
	eval := SuiteWorkspaceEvaluationOptions{Workspace: root, ManagedPass: true, ToContext: "kind-test", Inference: "provider:inference"}
	lift := WorkspaceLiftOptions{Workspace: root, Evaluation: eval, Destination: ReleaseLiftOptions{ToContext: "remote-prod", Inference: "provider:inference"}, EvalName: "dev", DestinationName: "prod", Repository: "registry.invalid/agents", PlanOnly: true}
	push := SuiteWorkspacePushOptions{Workspace: root, Reference: "suite:v1", Layout: filepath.Join(t.TempDir(), "layout")}
	for name, run := range map[string]func() error{
		"export": func() error {
			return a.ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: root, Member: suite.MemberID, Out: out})
		},
		"eval": func() error {
			p, e := PrepareSuiteWorkspaceEvaluation(t.Context(), eval)
			if p != nil {
				p.Close()
			}
			return e
		},
		"lift": func() error {
			p, e := PrepareWorkspaceLift(t.Context(), lift)
			if p != nil {
				p.Close()
			}
			return e
		},
		"push": func() error { _, e := a.PushSuiteWorkspaceManaged(t.Context(), push); return e },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "packaging policy") || !strings.Contains(err.Error(), "move aside") {
				t.Fatalf("migration remedy was lost: %v", err)
			}
			if (name == "export" || name == "eval") && strings.Contains(err.Error(), root) {
				t.Fatal("redacted preflight exposed private path", err)
			}
		})
	}
	for _, path := range []string{out, push.Layout} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("migration refusal wrote output", path, err)
		}
	}
	current, err := os.ReadFile(record)
	if err != nil || !bytes.Equal(legacy, current) {
		t.Fatal("legacy cache changed", err)
	}
	if err := os.Rename(entry, entry+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := a.ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: root, Member: suite.MemberID, Out: out}); err != nil {
		t.Fatal("unchanged-S public proof was invalidated", err)
	}
	result, err := a.PushSuiteWorkspaceManaged(t.Context(), push)
	if err != nil || result.Descriptor.Digest.String() != suite.Digest {
		t.Fatal("unchanged-S managed proof was invalidated", err)
	}
	receipt := filepath.Join(privateSuiteDir(t), "receipt.json")
	if err := os.WriteFile(receipt, raw, 0600); err != nil {
		t.Fatal(err)
	}
	push.Evaluation, push.Layout = receipt, filepath.Join(t.TempDir(), "layout")
	if result, err := a.PushSuiteWorkspaceManaged(t.Context(), push); err != nil || result.Descriptor.Digest.String() != suite.Digest {
		t.Fatal("unchanged-S explicit proof was invalidated", err)
	}
}
