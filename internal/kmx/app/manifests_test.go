package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
)

// kmx applies manifests from inside the binary, because it is installed with
// `go install` and run outside a clone. Two things can silently break that: a
// mistyped embed pattern (the file is simply absent at run time, on the
// operator's machine, half way through `kmx up`), and an edit to k8s/ that
// nobody rebuilt against. Assert both here, where it costs nothing.
func TestEmbeddedManifestsAreTheOnesInTheTree(t *testing.T) {
	for _, name := range []string{"ollama.yaml", "kagent-values.yaml", "hello-world.yaml", "tools-agent.yaml"} {
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

// Milestone 2 puts the plane's manifests and the two governed presets in the
// binary, for the same reason as the runtime ones: `kmx plane` and
// `kmx govern` run outside a clone, with no k8s/ on disk to point kubectl at.
func TestThePlanesManifestsTravelInTheBinary(t *testing.T) {
	for _, name := range []string{
		"plane/namespace.yaml", "plane/postgres.yaml", "plane/proxy.yaml",
		"plane/upstreams.yaml", "plane/network-policy.yaml",
		"models/governed-ollama.yaml", "models/governed-copilot.yaml",
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

// Every model preset names a Secret without embedding its credential value.
func TestModelPresetsTravelInTheBinary(t *testing.T) {
	var names []string
	presets, err := os.ReadDir(filepath.Join("..", "..", "..", "k8s", "models"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range presets {
		names = append(names, "models/"+e.Name())
	}
	for _, name := range names {
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

// Every preset in the tree is a preset `kmx use` will name, and nothing
// else is. This is the list an operator sees when they mistype one, so a
// preset added to k8s/models/ that never reached the binary would be
// advertised and then fail to apply.
func TestUseOffersExactlyTheEmbeddedPresets(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "k8s", "models"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		want = append(want, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	// Sorted on the NAME, not the filename: ReadDir orders
	// "openai-compatible.yaml" before "openai.yaml" ('-' sorts below '.'),
	// and what an operator is offered is the name.
	sort.Strings(want)
	got := presetNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("kmx use offers %v, k8s/models/ holds %v", got, want)
	}
	for _, preset := range got {
		name, err := presetManifest(preset)
		if err != nil {
			t.Errorf("preset %q is offered but does not resolve: %v", preset, err)
			continue
		}
		if _, err := manifest(name); err != nil {
			t.Errorf("preset %q resolves to %s, which is not embedded: %v", preset, name, err)
		}
	}
	// And a name that is not a preset is refused rather than turned into a
	// path: the preset name reaches both the embedded filesystem and the
	// object the agent is patched onto.
	for _, bad := range []string{"", "../plane/proxy", "nope", "governed-ollama.yaml"} {
		if _, err := presetManifest(bad); err == nil {
			t.Errorf("presetManifest(%q) was accepted", bad)
		}
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
