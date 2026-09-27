package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
)

// kmx applies manifests from inside the binary, because it is installed with
// `go install` and run outside a clone. Two things can silently break that: a
// mistyped embed pattern (the file is simply absent at run time, on the
// operator's machine, half way through `kmx up`), and an edit to k8s/ that
// nobody rebuilt against. Assert both here, where it costs nothing.
func TestEmbeddedManifestsAreTheOnesInTheTree(t *testing.T) {
	for _, name := range []string{"ollama.yaml", "orka-k8s-tool.yaml"} {
		embedded, err := manifest(name)
		if err != nil {
			t.Errorf("k8s/%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "k8s", name))
		if err != nil {
			t.Fatalf("k8s/%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from the embedded copy", name)
		}
	}
}

// `kmx plane` runs outside a clone, with no k8s/ on disk to point kubectl at,
// so its manifests travel in the binary.
func TestThePlanesManifestsTravelInTheBinary(t *testing.T) {
	for _, name := range []string{
		"plane/namespace.yaml", "plane/postgres.yaml", "plane/proxy.yaml",
		"plane/upstreams.yaml", "plane/network-policy.yaml",
	} {
		embedded, err := manifest(name)
		if err != nil {
			t.Errorf("k8s/%s is not embedded in the binary: %v", name, err)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("..", "..", "..", "k8s", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("k8s/%s: %v", name, err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from the embedded copy", name)
		}
	}
}

// The legacy runtime's manifests are gone from the tree AND from the binary.
// A file deleted from k8s/ but left in an embed pattern is a build error; a
// file left in k8s/ and dropped from the pattern is the silent half, and the
// walk below would catch that. This catches the third case: a manifest that
// survives somewhere nobody looks.
func TestTheLegacyRuntimesManifestsAreGone(t *testing.T) {
	for _, name := range []string{"kagent-values.yaml", "hello-world.yaml", "tools-agent.yaml",
		"models/ollama.yaml", "models/governed-ollama.yaml", "models/governed-copilot.yaml"} {
		if _, err := os.Stat(filepath.Join("..", "..", "..", "k8s", filepath.FromSlash(name))); err == nil {
			t.Errorf("k8s/%s is still in the tree", name)
		}
		if _, err := manifest(name); err == nil {
			t.Errorf("k8s/%s is still embedded in the binary", name)
		}
	}
	if _, err := os.Stat(filepath.Join("..", "..", "..", "k8s", "models")); err == nil {
		t.Error("k8s/models still exists; every preset in it was a kagent v1alpha2 ModelConfig")
	}
}

// Gateway/scenario retirement leaves no unembedded manifests. Check the
// positive boundary across both filesystems rather than retaining exclusions
// for files that no longer exist.
func TestAllRetainedManifestsTravelInTheBinary(t *testing.T) {
	root := filepath.Join("..", "..", "..", "k8s")
	files := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		embedded, err := kaimahi.Manifests.ReadFile("k8s/" + name)
		if err != nil {
			embedded, err = kaimahi.Managed.ReadFile("k8s/" + name)
		}
		if err != nil {
			t.Errorf("k8s/%s is not embedded: %v", name, err)
			return nil
		}
		onDisk, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("k8s/%s differs from its embedded copy", name)
		}
		files++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no retained manifests were checked")
	}
}
