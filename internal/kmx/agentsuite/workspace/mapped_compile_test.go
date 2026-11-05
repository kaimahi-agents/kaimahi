package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func mappedParent(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func compileFixture(t *testing.T, input MappedInput) *CompiledPackage {
	t.Helper()
	p, err := CompileMapped(t.Context(), input, mappedParent(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func TestCompiledPackageEmptyUseRefused(t *testing.T) {
	for name, p := range map[string]*CompiledPackage{"nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "unused")
			if p.Descriptor().Digest != "" || p.Fingerprint() != "" {
				t.Fatal("empty object exposed metadata")
			}
			if _, err := p.Load(t.Context()); !errors.Is(err, errCompiledUnavailable) {
				t.Fatalf("Load: %v", err)
			}
			if _, err := p.Sources(t.Context()); !errors.Is(err, errCompiledUnavailable) {
				t.Fatalf("Sources: %v", err)
			}
			if _, err := p.CopyToLayout(t.Context(), destination); !errors.Is(err, errCompiledUnavailable) {
				t.Fatalf("Copy: %v", err)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("empty use created destination: %v", err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompileMappedDeterministicAndExact(t *testing.T) {
	input := mappedFixture()
	input.Cases = nil
	for n := 0; n < 128; n++ {
		input.Cases = append(input.Cases, runtime.EvaluationCase{ID: fmt.Sprintf("case-%03d", n), Input: "Ada", ExpectContains: []string{"Ada"}})
	}
	a, b := compileFixture(t, input), compileFixture(t, input)
	if a.Descriptor().Digest != b.Descriptor().Digest || a.Fingerprint() != b.Fingerprint() {
		t.Fatal("location changed S/F")
	}
	suite, err := a.Load(t.Context())
	if err != nil || len(suite.Cases) != 128 {
		t.Fatal("case inventory changed", err)
	}
	for n, pc := range suite.Cases {
		c, err := DecodePackagedCase(pc.Bytes)
		if err != nil || c.ID != input.Cases[n].ID {
			t.Fatal("case order lost", n, err)
		}
	}
	sources, err := a.Sources(t.Context())
	if err != nil || !reflect.DeepEqual(sources, input.Sources) {
		t.Fatal("originals changed", err)
	}
	sources.Agent[0] = 'X'
	suite.Instructions[0] = 'X'
	again, err := a.Load(t.Context())
	if err != nil || again.Instructions[0] == 'X' {
		t.Fatal("returned data aliases package", err)
	}
	changed := input
	changed.Sources.Agent = append(bytes.Clone(input.Sources.Agent), ' ')
	c := compileFixture(t, changed)
	if c.Descriptor().Digest == a.Descriptor().Digest || c.Fingerprint() == a.Fingerprint() {
		t.Fatal("source formatting omitted")
	}
}

func TestCompileMappedExactTransfer(t *testing.T) {
	p := compileFixture(t, mappedFixture())
	descriptor := p.Descriptor()
	fingerprint := p.Fingerprint()
	out := filepath.Join(mappedParent(t), "published")
	if _, err := p.CopyToLayout(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	names, err := os.ReadDir(filepath.Join(out, "blobs/sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		before, err := os.ReadFile(filepath.Join(p.stage, "layout/blobs/sha256", name.Name()))
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(out, "blobs/sha256", name.Name()))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("blob changed", err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := suiteoras.ReadArtifactLayout(t.Context(), out, descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	sources, err := ReadSources(t.Context(), a)
	if err != nil || !reflect.DeepEqual(sources, mappedFixture().Sources) {
		t.Fatal("source-free roundtrip", err)
	}
	got, err := mappedFingerprint(a.Files())
	if err != nil || got != fingerprint {
		t.Fatal("F not reconstructable", err)
	}
	if _, err := p.Load(t.Context()); err == nil || p.Descriptor().Digest != "" || p.Fingerprint() != "" {
		t.Fatal("closed package still eligible", err)
	}
}

func TestCompileMappedOwnershipAndFailure(t *testing.T) {
	parent := mappedParent(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CompileMapped(ctx, mappedFixture(), parent); err == nil {
		t.Fatal("cancelled compilation succeeded")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled intake created state", err)
	}
	p, err := CompileMapped(t.Context(), mappedFixture(), parent)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(mappedParent(t), "occupied")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CopyToLayout(t.Context(), target); err == nil {
		t.Fatal("occupied destination replaced")
	}
	moved := p.stage + "-moved"
	if err := os.Rename(p.stage, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.stage, 0700); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(p.stage, "witness")
	os.WriteFile(witness, []byte("not ours"), 0600)
	if _, err := p.Load(t.Context()); err == nil {
		t.Fatal("substituted stage accepted")
	}
	if err := p.Close(); err == nil {
		t.Fatal("substitution hidden by Close")
	}
	if _, err := os.Stat(witness); err != nil {
		t.Fatal("Close deleted peer", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("Close not idempotent", err)
	}
}

func TestCompileMappedRequiredCloseFailure(t *testing.T) {
	p, err := CompileMapped(t.Context(), mappedFixture(), mappedParent(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err == nil {
		t.Fatal("required Close error ignored")
	}
	if _, err := p.Load(t.Context()); err == nil {
		t.Fatal("failed Close did not consume use")
	}
}
