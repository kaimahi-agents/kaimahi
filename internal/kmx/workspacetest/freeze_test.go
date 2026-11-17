package workspacetest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func workspaceFixture(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "workspace")
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := os.ReadFile(filepath.Join("..", "agentsuite", "workspace", "default-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	write("workspace.yaml", []byte("schemaVersion: 1\nname: greeter\nmember: agents/greeter/member.yaml\nbuildProfile: build-profiles/default.json\n"))
	write("agents/greeter/member.yaml", []byte("id: greeter\ndescription: Questions\ninstructions: instructions.md\nmodel:\n  protocol: openai-compatible\n  model: qwen-3.5-2b\nruntime:\n  id: orka\n"))
	write("agents/greeter/instructions.md", []byte("Answer briefly.\n"))
	write("build-profiles/default.json", profile)
	write("eval/example.yaml", []byte("id: example\ninput: What is 2+2?\nexpectContains: ['4']\n"))
	return root
}

func TestFreezeOrkaCleansValidationStage(t *testing.T) {
	root := workspaceFixture(t)
	base := filepath.Dir(root)
	p, err := FreezeOrka(t.Context(), Summary{Workspace: root, Runtime: "orka", Context: "remote", Namespace: "orka-system", Inference: "provider:model"}, filepath.Join(base, "validation"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	summary := p.Summary()
	if summary.SuiteDigest == "" || summary.SourceFingerprint == "" {
		t.Fatalf("frozen identity = %+v", summary)
	}
	suite, err := p.Suite(t.Context())
	if err != nil || suite.Digest != summary.SuiteDigest || suite.Runtime.ID != "orka" {
		t.Fatalf("suite = %+v, %v", suite, err)
	}
	entries, err := os.ReadDir(filepath.Join(base, "validation"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "lock" {
		t.Fatalf("validation residue = %v, %v", entries, err)
	}
}

func TestFreezeOrkaDetectsSourceReplacement(t *testing.T) {
	root := workspaceFixture(t)
	p, err := FreezeOrka(t.Context(), Summary{Workspace: root, Runtime: "orka", Context: "remote", Namespace: "orka-system", Inference: "provider:model"}, filepath.Join(filepath.Dir(root), "validation"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	name := filepath.Join(root, "agents", "greeter", "instructions.md")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	replacement := name + ".replacement"
	if err := os.WriteFile(replacement, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, name); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCurrent(t.Context()); err == nil {
		t.Fatal("same-byte source replacement accepted")
	}
}

func TestFreezeOrkaPreCancelledHasNoState(t *testing.T) {
	root := workspaceFixture(t)
	validation := filepath.Join(filepath.Dir(root), "validation")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := FreezeOrka(ctx, Summary{Workspace: root, Runtime: "orka", Context: "remote", Namespace: "orka-system", Inference: "provider:model"}, validation); err == nil {
		t.Fatal("pre-cancelled freeze succeeded")
	}
	if _, err := os.Stat(validation); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled freeze created state: %v", err)
	}
}
