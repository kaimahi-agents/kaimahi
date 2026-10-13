package workspace_test

import (
	"path/filepath"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestLoadArtifactPreservesDeclaredRuntime(t *testing.T) {
	for _, profile := range []workspace.RuntimeProfile{{ID: "orka"}, {ID: "kagent", Version: "v0.10.3", Engine: "go"}} {
		t.Run(profile.ID, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			instructions := filepath.Join(t.TempDir(), "instructions.md")
			writeFile(t, instructions, "Answer briefly.\n")
			if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "exact-model", InstructionsFile: instructions, Runtime: profile}); err != nil {
				t.Fatal(err)
			}
			p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
			if err != nil {
				t.Fatal(err)
			}
			a, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			member, err := workspace.LoadArtifact(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			if member.Runtime != profile || member.Digest != p.Descriptor.Digest.String() {
				t.Fatalf("runtime or digest substituted: %+v", member)
			}
		})
	}
}
