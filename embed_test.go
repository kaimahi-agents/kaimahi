package kaimahi

import (
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedFilesystemsCarryOnlyNativeAssets(t *testing.T) {
	for name, tc := range map[string]struct {
		assets fs.FS
		want   []string
	}{
		"Manifests": {
			assets: Manifests,
			want: []string{
				"k8s/ollama.yaml",
				"k8s/orka-k8s-tool.yaml",
				"scripts/orka-k8s-tool.py",
			},
		},
		"Managed": {
			assets: Managed,
			want: []string{
				"scripts/aks-down.sh",
				"scripts/aks-up.sh",
				"scripts/kube-guard.sh",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			err := fs.WalkDir(tc.assets, ".", func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					got = append(got, path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("embedded files = %v, want exactly %v", got, tc.want)
			}
		})
	}
}

func TestMakeBuildTracksOnlyNativeEmbeddedAssets(t *testing.T) {
	// Evaluate Make's real asset list without building or modifying the tree.
	cmd := exec.Command("make", "--no-print-directory", "TARGET=kind",
		"-f", "Makefile", "-f", "-", "print-kmx-assets")
	cmd.Stdin = strings.NewReader("print-kmx-assets:\n\t@printf '%s\\n' '$(KMX_ASSETS)'\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluate Make embedded inputs: %v\n%s", err, output)
	}
	got := strings.Fields(string(output))
	slices.Sort(got)
	want := []string{
		"k8s/ollama.yaml",
		"k8s/orka-k8s-tool.yaml",
		"scripts/aks-down.sh",
		"scripts/aks-up.sh",
		"scripts/kube-guard.sh",
		"scripts/orka-k8s-tool.py",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Make embedded inputs = %v, want exactly %v", got, want)
	}
}

func TestEmbeddedAssetsCanBeRead(t *testing.T) {
	for name, assets := range map[string]fs.FS{
		"Manifests": Manifests,
		"Managed":   Managed,
	} {
		t.Run(name, func(t *testing.T) {
			files := 0
			err := fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type().IsRegular() {
					files++
					_, err = fs.ReadFile(assets, path)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if files == 0 {
				t.Fatal("embedded filesystem contains no files")
			}
		})
	}
}
