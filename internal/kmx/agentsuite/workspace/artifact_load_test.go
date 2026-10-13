package workspace_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestLoadArtifactWithoutSourceFingerprint(t *testing.T) {
	root, cache := createWorkspace(t)
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	want, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	// A transferred graph may live alongside unrelated references; no source
	// root, name, preparation record or fake fingerprint is supplied to LoadArtifact.
	layout := filepath.Join(t.TempDir(), "published")
	if _, err := suiteoras.PushLayout(t.Context(), p.Path, layout, "greeter:v1", p.Descriptor); err != nil {
		t.Fatal(err)
	}
	a, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	got, err := workspace.LoadArtifact(t.Context(), a)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("source-free loader changed packaged subject", err)
	}
	got.Instructions[0] = 'X'
	got.Cases[0].Bytes[0] = 'X'
	fresh, err := workspace.LoadArtifact(t.Context(), a)
	if err != nil || !reflect.DeepEqual(fresh, want) {
		t.Fatal("caller modified retained packaged bytes", err)
	}
}

func TestLoadArtifactRefusesPublicOnlyProfile(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := suiteoras.Push(t.Context(), filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal"), layout, "minimal:v1")
	if err != nil {
		t.Fatal(err)
	}
	a, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadArtifact(t.Context(), a); err == nil || err.Error() != "packaged native suite verification failed" {
		t.Fatalf("public graph authorized native execution: %v", err)
	}
	if _, err := workspace.LoadArtifact(t.Context(), nil); err == nil {
		t.Fatal("nil artifact accepted")
	}
}
