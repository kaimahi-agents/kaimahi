package kaimahi

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	makefile := filepath.Join(t.TempDir(), "inherited.mk")
	if err := os.WriteFile(makefile, []byte("override KMX_ASSETS := inherited-makefiles-must-not-leak\n"), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"k8s/ollama.yaml",
		"k8s/orka-k8s-tool.yaml",
		"scripts/aks-down.sh",
		"scripts/aks-up.sh",
		"scripts/kube-guard.sh",
		"scripts/orka-k8s-tool.py",
	}
	for _, tc := range []struct {
		name, makeflags, mflags, makelevel, makefiles string
	}{
		{name: "clean"},
		{
			name: "inherited-jobserver", makeflags: "-j2 --jobserver-auth=3,4",
			mflags: "-j2 --jobserver-auth=3,4", makelevel: "1",
		},
		{
			name: "inherited-makefiles", makeflags: "-j2 --jobserver-auth=3,4",
			mflags: "-j2 --jobserver-auth=3,4", makelevel: "1", makefiles: makefile,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAKEFLAGS", tc.makeflags)
			t.Setenv("MFLAGS", tc.mflags)
			t.Setenv("MAKELEVEL", tc.makelevel)
			t.Setenv("MAKEFILES", tc.makefiles)

			// Evaluate Make's real asset list without building or modifying the tree.
			cmd := exec.Command("make", "--no-print-directory", "TARGET=kind",
				"-f", "Makefile", "-f", "-", "print-kmx-assets")
			cmd.Stdin = strings.NewReader("print-kmx-assets:\n\t@printf '%s\\n' '$(KMX_ASSETS)'\n")
			// Parent Make state must not affect this standalone metadata query.
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch key {
				case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEFILES":
					continue
				default:
					cmd.Env = append(cmd.Env, entry)
				}
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("evaluate Make embedded inputs: %v\nstdout:\n%s\nstderr:\n%s", err, output, &stderr)
			}
			got := strings.Fields(string(output))
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("Make embedded inputs = %v, want exactly %v\nstderr:\n%s", got, want, &stderr)
			}
		})
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
