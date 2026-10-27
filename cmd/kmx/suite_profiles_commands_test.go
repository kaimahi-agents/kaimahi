package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentkit "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func profileCommandSource(t *testing.T) (string, *workspace.Prepared) {
	t.Helper()
	root, _ := suiteEvalSource(t)
	write := func(p string, b []byte) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write("agents/greeter/member.yaml", []byte(strings.Split(string(read("agents/greeter/member.yaml")), "\nruntime:")[0]+"\n"))
	var bp suitecore.BuildProfile
	if err := suitecore.Unmarshal(read("build-profiles/default.json"), &bp); err != nil {
		t.Fatal(err)
	}
	rows := ""
	for _, id := range []string{"cluster", "orka", "sessions"} {
		rp := suitecore.RuntimeProfile{SchemaVersion: suitecore.SpecVersion, MediaType: suitecore.MediaTypeRuntimeProfile, ID: id, Runtime: suitecore.EvidenceImplementation{ID: id, Version: "test-only", Digest: bp.RuntimeBase[0].Image.Digest}, Harness: suitecore.EvidenceImplementation{ID: "test-harness", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, Renderer: suitecore.EvidenceImplementation{ID: "test-renderer", Version: "test-only", Digest: bp.Harness[0].Image.Digest}, ConfigurationABI: "test-only", ImageRequired: true}
		b, err := suitecore.Marshal(rp)
		if err != nil {
			t.Fatal(err)
		}
		write("runtime-profiles/"+id+".json", b)
		rows += fmt.Sprintf("  - runtimeProfile: runtime-profiles/%s.json\n    buildProfile: build-profiles/default.json\n    platform: linux/amd64\n", id)
	}
	write("workspace.yaml", []byte("schemaVersion: 3\nname: greeter\nmember: agents/greeter/member.yaml\nprofiles:\n"+rows))
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return root, p
}

func TestProfiledCLITransferAndOfflineRefusals(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, p := profileCommandSource(t)
	t.Setenv("PATH", t.TempDir())
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	for _, id := range []string{"cluster", "orka", "sessions"} {
		layout := filepath.Join(t.TempDir(), "layout")
		if err := execute([]string{"package", "push", "--workspace", root, "--runtime-profile", id, "--platform", "linux/amd64", "--unevaluated", "--to-layout", layout, "suite:v1"}, deps); err != nil {
			t.Fatal(err)
		}
		artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
		if err != nil {
			t.Fatal("not exact S", err)
		}
		if _, err := workspace.LoadArtifactSelected(t.Context(), artifact, workspace.Selection{RuntimeProfile: "sessions"}); err != nil {
			t.Fatal("transfer dropped other profile", err)
		}
	}
	for _, flags := range [][]string{{"--unevaluated"}, {"--runtime-profile", "missing", "--unevaluated"}, {"--runtime-profile", "orka"}, {"--runtime-profile", "orka", "--platform", "linux/arm64", "--unevaluated"}, {"--runtime-profile", ""}, {"--platform", ""}} {
		out.Reset()
		diag.Reset()
		layout := filepath.Join(t.TempDir(), "layout")
		args := append([]string{"package", "push", "--workspace", root, "--to-layout", layout, "suite:v1"}, flags...)
		if err := execute(args, deps); err == nil || out.Len() != 0 {
			t.Fatal("invalid selection published", flags, err)
		}
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatal("refusal changed destination")
		}
	}
	for _, args := range [][]string{{"eval", "--workspace", root, "--to-context", "unreachable", "--inference", "provider:inference"}, {"eval", "export", "--workspace", root, "--member", "greeter", "--out", filepath.Join(t.TempDir(), "evidence.json")}, {"eval", "--runtime-profile", "orka"}, {"lift", "--runtime-profile", "orka"}} {
		if err := execute(args, deps); err == nil {
			t.Fatal("profile live/export enabled", args)
		}
	}
	if *loads != 0 {
		t.Fatal("profile offline refusal loaded operational config", *loads)
	}
}
func TestProfiledCLIBuilderSelectionAndInertHelp(t *testing.T) {
	root, p := profileCommandSource(t)
	outDir := filepath.Join(t.TempDir(), "suite")
	if _, err := suiteoras.Pull(t.Context(), p.Path, p.Descriptor.Digest.String(), outDir); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	calls := 0
	deps.newAgentKitBuilder = func(agentkit.Options) kmxsuite.SandboxBuilder {
		return sandboxBuilderFunc(func(_ context.Context, plan kmxsuite.SandboxPlan, _ io.Writer) (kmxsuite.BuildResult, error) {
			calls++
			if plan.RuntimeProfile.ID != "sessions" || plan.Composition.RuntimeProfile != "sessions" {
				t.Fatal("wrong profile passed to builder")
			}
			return kmxsuite.BuildResult{}, fmt.Errorf("selected offline boundary")
		})
	}
	for _, sel := range [][]string{{}, {"--runtime-profile", "missing"}, {"--runtime-profile", "sessions"}} {
		args := append([]string{"package", "build", outDir, "--output", filepath.Join(t.TempDir(), "image.tar"), "--model-base-url", "https://models.example.test/v1"}, sel...)
		err := execute(args, deps)
		if err == nil {
			t.Fatal("build unexpectedly completed")
		}
		if len(sel) > 0 && sel[1] == "sessions" && !strings.Contains(err.Error(), "selected offline boundary") {
			t.Fatal(err)
		}
	}
	if calls != 1 || *loads != 0 {
		t.Fatal("selection/config boundary", calls, *loads)
	}
	for _, args := range [][]string{{"package", "push", "--help"}, {"package", "build", "--help"}, {"__complete", "package", "push", "--runtime-profile", ""}, {"__complete", "package", "build", "--runtime-profile", ""}} {
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || *loads != 0 {
		t.Fatal("help/completion had effects")
	}
	// New selectors are deliberately absent from live and extracted-transfer paths.
	for _, args := range [][]string{{"package", "suite", "push", outDir, "suite:v1", "--runtime-profile", "sessions"}, {"eval", "--workspace", root, "--platform", "linux/amd64"}} {
		if err := execute(args, deps); err == nil {
			t.Fatal("unsupported flag accepted")
		}
	}
}
