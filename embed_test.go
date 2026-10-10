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
	"time"
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

func TestMakeBuildRefreshesCompiledInputs(t *testing.T) {
	t.Setenv("KMX", filepath.Join(t.TempDir(), "inherited-kmx"))
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, before, after, wantBefore, wantAfter, target, output string
	}{
		{
			name: "package-source", path: "pkg/kmx/value.go", target: "bin/kmx",
			before:     "package kmx\nvar Value = \"old\"\n",
			after:      "package kmx\nvar Value = \"new\"\n",
			wantBefore: "old|old|old|old", wantAfter: "new|old|old|old",
		},
		{
			name: "credential-data", path: "internal/kmx/secretshapes/shapes.json",
			before: "old", after: "new",
			wantBefore: "old|old|old|old", wantAfter: "old|new|old|old",
		},
		{
			name: "schema-data", path: "internal/kmx/orkaschema/fixtures/v0.2.0/agents.yaml",
			before: "old", after: "new", target: "out/kmx", output: "out/kmx",
			wantBefore: "old|old|old|old", wantAfter: "old|old|new|old",
		},
		{
			name: "root-embedded-asset", path: "k8s/ollama.yaml",
			before: "old", after: "new",
			wantBefore: "old|old|old|old", wantAfter: "old|old|old|new",
		},
		{
			name: "added-source", path: "pkg/kmx/extra.go",
			after:      "package kmx\nfunc init() { Value = \"added\" }\n",
			wantBefore: "old|old|old|old", wantAfter: "added|old|old|old",
		},
		{
			name: "deleted-source", path: "pkg/kmx/extra.go",
			before:     "package kmx\nfunc init() { Value = \"extra\" }\n",
			wantBefore: "extra|old|old|old", wantAfter: "old|old|old|old",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, text string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0644); err != nil {
					t.Fatal(err)
				}
			}
			// Use real Make and Go on a tiny module, not this checkout's binary.
			files := map[string]string{
				"Makefile": string(makefile),
				"go.mod":   "module example.invalid/makefixture\n\ngo 1.20\n",
				"embed.go": "package makefixture\nimport _ \"embed\"\n//go:embed k8s/ollama.yaml\nvar Model string\n",
				"cmd/kmx/main.go": `package main
import (
    "fmt"
    assets "example.invalid/makefixture"
    "example.invalid/makefixture/pkg/kmx"
    "example.invalid/makefixture/internal/kmx/secretshapes"
    "example.invalid/makefixture/internal/kmx/orkaschema"
)
func main() { fmt.Printf("%s|%s|%s|%s", kmx.Value, secretshapes.Data, orkaschema.Data, assets.Model) }
`,
				"pkg/kmx/value.go":                                    "package kmx\nvar Value = \"old\"\n",
				"internal/kmx/secretshapes/shapes.go":                 "package secretshapes\nimport _ \"embed\"\n//go:embed shapes.json\nvar Data string\n",
				"internal/kmx/secretshapes/shapes.json":               "old",
				"internal/kmx/orkaschema/schema.go":                   "package orkaschema\nimport \"embed\"\n//go:embed fixtures/*/*.yaml\nvar data embed.FS\nvar content, _ = data.ReadFile(\"fixtures/v0.2.0/agents.yaml\")\nvar Data = string(content)\n",
				"internal/kmx/orkaschema/fixtures/v0.2.0/agents.yaml": "old",
				"k8s/ollama.yaml":                                     "old", "k8s/orka-k8s-tool.yaml": "old",
				"scripts/orka-k8s-tool.py": "old", "scripts/aks-up.sh": "old",
				"scripts/aks-down.sh": "old", "scripts/kube-guard.sh": "old",
			}
			if tc.before != "" {
				files[tc.path] = tc.before
			}
			for name, text := range files {
				write(name, text)
			}
			// Keep unchanged inputs older than the binary even on coarse clocks.
			old := time.Now().Add(-time.Hour)
			for name := range files {
				if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
					t.Fatal(err)
				}
			}
			outputPath := "bin/kmx"
			if tc.output != "" {
				outputPath = tc.output
			}
			build := func() {
				t.Helper()
				args := []string{"--no-print-directory", "TARGET=kind"}
				if tc.output != "" {
					args = append(args, "KMX="+tc.output)
				}
				if tc.target != "" {
					args = append(args, tc.target)
				}
				cmd := exec.Command("make", args...)
				cmd.Dir = root
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					switch key {
					case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEFILES", "KMX", "GOWORK", "GOFLAGS", "GOOS", "GOARCH":
						continue
					default:
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "GOWORK=off", "GOFLAGS=")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("make build: %v\n%s", err, output)
				}
			}
			check := func(want string) {
				t.Helper()
				output, err := exec.Command(filepath.Join(root, outputPath)).CombinedOutput()
				if err != nil {
					t.Fatalf("run built binary: %v\n%s", err, output)
				}
				if string(output) != want {
					t.Fatalf("built binary reports %q, want %q", output, want)
				}
			}
			build()
			check(tc.wantBefore)
			if tc.after == "" {
				if err := os.Remove(filepath.Join(root, tc.path)); err != nil {
					t.Fatal(err)
				}
			} else {
				write(tc.path, tc.after)
			}
			build()
			check(tc.wantAfter)
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
