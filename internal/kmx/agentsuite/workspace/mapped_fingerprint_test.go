package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
)

func TestMappedArtifactFingerprintAfterCompilerClose(t *testing.T) {
	p := compileFixture(t, mappedFixture())
	want := p.Fingerprint()
	descriptor := p.Descriptor()
	target := filepath.Join(mappedParent(t), "retained-copy")
	if _, err := p.CopyToLayout(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), target, descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := MappedArtifactFingerprint(t.Context(), artifact); err != nil || got != want {
		t.Fatalf("retained F = %q, %v; want %q", got, err, want)
	}
	changed, _ := sourceArtifact(t, func(files map[string][]byte) {
		// Rebuild a valid source profile with a formatting-only original change.
		input := mappedFixture()
		input.Sources.Agent = append(input.Sources.Agent, ' ')
		frozen, err := freezeMapped(input)
		if err != nil {
			t.Fatal(err)
		}
		s, err := resolveMapped(t.Context(), frozen)
		if err != nil {
			t.Fatal(err)
		}
		clear(files)
		for name, data := range s.files {
			files[name] = data
		}
	})
	if got, err := MappedArtifactFingerprint(t.Context(), changed); err != nil || got == want {
		t.Fatalf("formatting F = %q, %v", got, err)
	}
}

func TestMappedArtifactFingerprintRefusals(t *testing.T) {
	good, _ := sourceArtifact(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := MappedArtifactFingerprint(ctx, good); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled F = %q, %v", got, err)
	}
	if got, err := MappedArtifactFingerprint(t.Context(), nil); got != "" || err == nil {
		t.Fatalf("nil F = %q, %v", got, err)
	}
	for _, kind := range []string{"legacy", "extra file"} {
		t.Run(kind, func(t *testing.T) {
			artifact, _ := sourceArtifact(t, func(files map[string][]byte) {
				switch kind {
				case "legacy":
					for name := range files {
						if name == localartifact.SourceIndexPath || name == "metadata/kmx/source/greeter.agent.md" || name == "metadata/kmx/source/greeter.agent.test.md" {
							delete(files, name)
						}
					}
				case "extra file":
					files["extra.txt"] = []byte("not in the closed graph")
				}
			})
			if got, err := MappedArtifactFingerprint(t.Context(), artifact); got != "" || err == nil {
				t.Fatalf("invalid F = %q, %v", got, err)
			}
		})
	}
}
