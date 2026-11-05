package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func cliSourcePackage(t *testing.T) (string, workspace.SourceBundle) {
	t.Helper()
	sources := workspace.SourceBundle{Format: "kmx.orka-agent-markdown", SchemaVersion: 1, Agent: []byte("# Original agent\r\n"), Tests: []byte("# Original tests\r\n")}
	input := workspace.MappedInput{SuiteName: "greeter", MemberID: "greeter", Model: "qwen-3.5-2b", Protocol: "openai-compatible", Runtime: workspace.RuntimeProfile{ID: "orka"}, Instructions: []byte("Answer briefly.\n"), Cases: []runtime.EvaluationCase{{ID: "greeting", Input: "Ada", ExpectContains: []string{"Ada"}}}, Sources: sources}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := workspace.CompileMapped(t.Context(), input, parent)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(parent, "layout")
	if _, err := p.CopyToLayout(t.Context(), layout); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	return layout, sources
}

func TestSourceProfileExistingArtifactCLI(t *testing.T) {
	layout, sources := cliSourcePackage(t)
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	pulled := filepath.Join(t.TempDir(), "pulled")
	if err := execute([]string{"package", "pull", "compiled", "--from-layout", layout, "--output", pulled}, deps); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"greeter.agent.md": sources.Agent, "greeter.agent.test.md": sources.Tests} {
		data, err := os.ReadFile(filepath.Join(pulled, "metadata/kmx/source", name))
		if err != nil || !bytes.Equal(data, want) {
			t.Fatal("pull changed original", name, err)
		}
	}
	if *loads != 0 {
		t.Fatal("artifact pull loaded runtime configuration")
	}
}
func TestSourceProfileNoHandleCapabilityOrWorkspaceBypass(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"package", "test", "--package-handle", strings.Repeat("a", 32)}, deps); err == nil || *loads != 0 {
		t.Fatal("unimplemented handle route reached config", err)
	}
	out.Reset()
	diag.Reset()
	deps = versionTestDependencies(&out, &diag)
	if err := execute([]string{"version", "-o", "json"}, deps); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("package.prepare")) || bytes.Contains(out.Bytes(), []byte("orka-source/v1")) {
		t.Fatal("unimplemented capability advertised")
	}
	layout, _ := cliSourcePackage(t)
	if err := execute([]string{"package", "test", "--workspace", layout}, deps); err == nil {
		t.Fatal("source artifact bypassed workspace intake")
	}
}
