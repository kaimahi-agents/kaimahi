package workspace_test

import (
	"errors"
	"path/filepath"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
)

func TestLoadArtifactPreservesDeclaredRuntime(t *testing.T) {
	for _, profile := range []workspace.RuntimeProfile{{ID: "orka"}, {ID: "kagent", Version: "v0.10.3", Engine: "go"}, {ID: "agentsessions", Version: sessionsVersion, Engine: "chat"}} {
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

func TestPublicRecipeDoesNotRequireRuntimeProfileSelection(t *testing.T) {
	root, cache := profiledWorkspace(t)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	public, err := suiteview.LoadLayout(t.Context(), p.Path, suiteview.Selection{ManifestDigest: p.Descriptor.Digest.String(), MemberID: "greeter"})
	if err != nil || public.Member.ID != "greeter" || public.Model.Model != "qwen-3.5-2b" || string(public.Instructions) != "Answer briefly.\n\nExact whitespace.\n" {
		t.Fatal("profile-independent public recipe changed", err, public)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadArtifact(t.Context(), artifact); !errors.Is(err, workspace.ErrProfileSelectionRequired) {
		t.Fatalf("public recipe authorized unselected native execution: %v", err)
	}
}
