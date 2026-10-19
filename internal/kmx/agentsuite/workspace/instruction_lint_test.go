package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	godigest "github.com/opencontainers/go-digest"
)

// A warning must not replace rejection, expose the match, or run for other fields.
func TestInstructionCredentialLintSource(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.What, func(t *testing.T) {
			root, cache := createWorkspace(t)
			name := filepath.Join(root, "agents/greeter/instructions.md")
			writeFile(t, name, shape.Example)
			_, err := workspace.Prepare(t.Context(), root, cache)
			assertInstructionWarning(t, err, shape.Example)
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("lint refusal created cache", err)
			}
			if string(readFile(t, name)) != shape.Example {
				t.Fatal("lint rewrote source")
			}
		})
	}
	for _, field := range []string{"description", "invalid Unicode"} {
		t.Run(field, func(t *testing.T) {
			root, cache := createWorkspace(t)
			if field == "description" {
				name := filepath.Join(root, "agents/greeter/member.yaml")
				writeFile(t, name, strings.ReplaceAll(string(readFile(t, name)), "Public questions", secretshapes.All()[0].Example))
			} else {
				writeFile(t, filepath.Join(root, "agents/greeter/instructions.md"), string([]byte{0xff}))
			}
			_, err := workspace.Prepare(t.Context(), root, cache)
			if err == nil || strings.Contains(err.Error(), "Warning:") {
				t.Fatal("non-instruction match or invalid Unicode produced lint warning", err)
			}
		})
	}
}

func assertInstructionWarning(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("credential-shaped instructions admitted")
	}
	text := err.Error()
	warning, refusal := strings.Index(text, "Warning:"), strings.Index(text, "refusing invalid Unicode or credential-shaped content")
	if warning < 0 || refusal <= warning || !strings.Contains(text, "redacted") || strings.Contains(text, secret) {
		t.Fatal("missing, late or unsafe instruction warning")
	}
}

func TestInstructionCredentialLintPackaged(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	secret := secretshapes.All()[0].Example
	writeFile(t, filepath.Join(out, "instructions/greeter.md"), secret)
	// Rebind the public descriptors, so public-format integrity succeeds and
	// the source-free native preflight must still refuse with a safe warning.
	rebindAgent(t, out, func(agent *agentsuite.Agent) {
		agent.Instructions.Descriptor.Digest = godigest.FromString(secret).String()
		agent.Instructions.Descriptor.Size = int64(len(secret))
	})
	published, err := suiteoras.Push(t.Context(), out, filepath.Join(t.TempDir(), "layout"), "test")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), published.Path, published.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.LoadArtifact(context.Background(), artifact)
	assertInstructionWarning(t, err, secret)
}
