package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"oras.land/oras-go/v2/content/oci"
)

func TestWorkspaceCreateAndPushAreOfflineAndPreserveDigest(t *testing.T) {
	root := t.TempDir()
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructions, []byte("Answer briefly.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CONTAINER_ENGINE", "not-an-engine")
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"agent", "create", "greeter", "--workspace", root, "--instructions", instructions, "--model", "qwen-3.5-2b"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 {
		t.Fatal("offline create loaded operational configuration")
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "greeter", "agent.yaml")); !os.IsNotExist(err) {
		t.Fatal("workspace create wrote a PortableAgent")
	}
	if _, err := os.Stat(filepath.Join(root, "workspace.yaml")); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "layout")
	out.Reset()
	if err := execute([]string{"suite", "push", "--workspace", root, "--to-layout", layout, "team:v1"}, deps); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	store, err := oci.New(layout)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := store.Resolve(context.Background(), "team:v1")
	if err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, descriptor.Digest)
	if diagnostics.String() != identity {
		t.Fatalf("workspace diagnostic = %q, want %q", diagnostics.String(), identity)
	}
	wantOut := fmt.Sprintf("Pushed native-Orka AgentSuite greeter to %s (%s)\nRequired member images: []\n", layout, descriptor.Digest)
	if first != wantOut {
		t.Fatalf("workspace result changed: %q, want %q", first, wantOut)
	}
	diagnostics.Reset()
	out.Reset()
	if err := execute([]string{"suite", "push", "--workspace", root, "--to-layout", layout, "team:v1"}, deps); err != nil {
		t.Fatal(err)
	}
	if out.String() != first || diagnostics.String() != identity {
		t.Fatalf("repeat publication changed result/diagnostic: %q, %q", out.String(), diagnostics.String())
	}
	if *loads != 0 {
		t.Fatal("workspace publication loaded operational configuration")
	}
	pulled := filepath.Join(t.TempDir(), "pulled")
	if err := execute([]string{"suite", "pull", "team:v1", "--from-layout", layout, "--output", pulled}, deps); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(pulled, "instructions", "greeter.md"))
	if err != nil || string(data) != "Answer briefly.\n" {
		t.Fatalf("packaged instructions = %q, %v", data, err)
	}
	profile, err := os.ReadFile(filepath.Join(pulled, "metadata", "kmx", "native-orka.json"))
	if err != nil || !bytes.Contains(profile, []byte(`"requiredImages":[]`)) {
		t.Fatalf("native profile = %s, %v", profile, err)
	}
}

func TestWorkspaceCreateRefusesModeConflictsBeforeConfigOrWrites(t *testing.T) {
	for _, flag := range []string{"namespace", "provider-type", "secret", "base-url", "out", "bundle-path", "tools", "task", "runtime", "no-apply", "dry-run", "context", "container-engine"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			args := []string{"agent", "create", "greeter", "--workspace", root, "--model", "qwen-3.5-2b", "--instructions", "missing-file", "--" + flag}
			if flag != "no-apply" && flag != "dry-run" {
				args = append(args, "unused")
			}
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "--"+flag) {
				t.Fatalf("conflict %s: %v", flag, err)
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatal("invalid workspace mode loaded config or emitted artifacts")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid create wrote files: %v, %v", entries, err)
			}
		})
	}
}

func TestWorkspaceSelectorsRejectEmptyAndAmbiguousSource(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "create", "greeter", "--workspace", ""},
		{"agent", "create", "--workspace", "."},
		{"suite", "push", "--workspace", "", "team:v1"},
		{"suite", "push", "directory", "team:v1", "--workspace", "."},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("selection %v: %v", args, err)
		}
		if *loads != 0 {
			t.Fatal("invalid source selection loaded config")
		}
	}
}

func TestWorkspaceRegistryPushProvenanceControls(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("Answer briefly.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(context.Background(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	server := suiteOutputRegistryFromLayout(t, prepared.Path)
	ref := strings.TrimPrefix(server.URL, "http://") + "/team/suite:prepared"
	for _, tc := range []struct {
		name              string
		flags             []string
		required, warning bool
	}{
		{"default", nil, false, true},
		{"opt-out", []string{"--provenance=false"}, false, false},
		{"required", []string{"--require-provenance"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "opt-out" {
				t.Setenv("PATH", t.TempDir())
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			args := append([]string{"suite", "push", "--workspace", root, ref, "--plain-http"}, tc.flags...)
			err := execute(args, deps)
			if tc.required {
				if err == nil || !strings.Contains(err.Error(), "tag was not changed") {
					t.Fatalf("required attachment failure: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				identity := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, prepared.Descriptor.Digest)
				if !strings.HasPrefix(diagnostics.String(), identity) || strings.Count(diagnostics.String(), "Workspace:") != 1 {
					t.Fatalf("registry workspace identity = %q; want %q once", diagnostics.String(), identity)
				}
				wantOut := fmt.Sprintf("Pushed native-Orka AgentSuite greeter to %s (%s)\nRequired member images: []\nPin this AgentSuite: %s@%s\n", ref, prepared.Descriptor.Digest, strings.TrimSuffix(ref, ":prepared"), prepared.Descriptor.Digest)
				if out.String() != wantOut {
					t.Fatalf("registry result changed: %q, want %q", out.String(), wantOut)
				}
				if got := strings.Contains(diagnostics.String(), "provenance was not attached"); got != tc.warning {
					t.Fatalf("default/opt-out warning = %q, want %t", diagnostics.String(), tc.warning)
				}
			}
			if *loads != 0 {
				t.Fatal("workspace provenance loaded operational configuration")
			}
		})
	}
}

func TestWorkspaceProvenanceFlagsRefuseConflictsBeforePreparation(t *testing.T) {
	for _, flags := range [][]string{
		{"--provenance=false", "--require-provenance"},
		{"--to-layout", "missing-layout", "--provenance=false"},
		{"--to-layout", "missing-layout", "--require-provenance"},
	} {
		state := t.TempDir()
		t.Setenv("KMX_HOME", state)
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		args := append([]string{"suite", "push", "--workspace", "missing-workspace", "team:v1"}, flags...)
		if err := execute(args, deps); err == nil || !strings.Contains(err.Error(), "provenance") {
			t.Fatalf("invalid provenance flags: %v", err)
		}
		entries, err := os.ReadDir(state)
		if err != nil || len(entries) != 0 || *loads != 0 {
			t.Fatal("invalid provenance flags prepared state or loaded config")
		}
	}
}

func TestWorkspacePathsRefuseCredentialShapesWithoutDisclosure(t *testing.T) {
	credential := "ghp_" + strings.Repeat("x", 36)
	for _, field := range []string{"workspace", "instructions"} {
		t.Run(field, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			input := filepath.Join(t.TempDir(), "instructions.md")
			if err := os.WriteFile(input, []byte("Answer briefly.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if field == "workspace" {
				root = filepath.Join(t.TempDir(), credential)
			} else {
				input = filepath.Join(t.TempDir(), credential)
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute([]string{"agent", "create", "greeter", "--workspace", root, "--model", "test-model", "--instructions", input}, deps)
			if err == nil || strings.Contains(err.Error(), credential) || strings.Contains(out.String(), credential) || strings.Contains(diagnostics.String(), credential) {
				t.Fatal("credential-shaped path was accepted or disclosed")
			}
			if *loads != 0 {
				t.Fatal("invalid workspace path loaded configuration")
			}
		})
	}
}

func TestWorkspaceProfileParseErrorsDoNotDiscloseContent(t *testing.T) {
	credential := "ghp_" + strings.Repeat("x", 36)
	escaped := "\\u0067" + credential[1:]
	for _, profile := range []string{
		`{"` + credential + `":"unused"}`,
		`{"` + escaped + `":"unused"}`,
		`{} "` + credential + `"`,
	} {
		root := t.TempDir()
		input := filepath.Join(t.TempDir(), "instructions.md")
		if err := os.WriteFile(input, []byte("Answer briefly.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KMX_HOME", t.TempDir())
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute([]string{"agent", "create", "greeter", "--workspace", root, "--model", "test-model", "--instructions", input}, deps); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "build-profiles/default.json"), []byte(profile), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		err := execute([]string{"suite", "push", "--workspace", root, "--to-layout", filepath.Join(t.TempDir(), "published"), "team:v1"}, deps)
		if err == nil || !strings.Contains(err.Error(), "profile") {
			t.Fatal("invalid profile should refuse with a profile diagnostic")
		}
		if strings.Contains(err.Error(), credential) || strings.Contains(out.String(), credential) || strings.Contains(diagnostics.String(), credential) {
			t.Fatal("profile parse diagnostic disclosed content")
		}
		if *loads != 0 {
			t.Fatal("invalid profile loaded target configuration")
		}
	}
}

func TestWorkspaceHelpAndCompletionHaveNoOperationalEffects(t *testing.T) {
	state := t.TempDir()
	t.Setenv("KMX_HOME", state)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("broken: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, args := range [][]string{
		{"agent", "create", "--help"}, {"suite", "push", "--help"},
		{"__complete", "agent", "create", "--workspace", ""},
		{"__complete", "suite", "push", "--workspace", ""},
		{"__complete", "suite", "push", ""},
		{"__complete", "suite", "push", "team:v1", ""},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		entries, err := os.ReadDir(state)
		if err != nil || len(entries) != 0 || *loads != 0 {
			t.Fatal("workspace help/completion prepared state or loaded operational config", err)
		}
	}
}

func TestWorkspacePushDiscoversFromNestedCwdAndHonorsOverride(t *testing.T) {
	for _, mode := range []string{"discovery", "relative override", "absolute override"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "workspace")
			input := filepath.Join(t.TempDir(), "instructions.md")
			if err := os.WriteFile(input, []byte("Answer briefly.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := workspace.Create(context.Background(), root, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input}); err != nil {
				t.Fatal(err)
			}
			start := filepath.Join(root, "nested", "deep")
			if mode != "discovery" {
				start = t.TempDir()
				if err := os.WriteFile(filepath.Join(start, "workspace.yaml"), []byte("broken: [\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(start, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(start)
			t.Setenv("KMX_HOME", t.TempDir())
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			args := []string{"suite", "push", "team:v1", "--to-layout", "layout"}
			if mode != "discovery" {
				override := root
				if mode == "relative override" {
					var err error
					override, err = filepath.Rel(start, root)
					if err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, "--workspace", override)
			}
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			store, err := oci.New(filepath.Join(start, "layout"))
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := store.Resolve(context.Background(), "team:v1")
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("Workspace: %q; AgentSuite: %s\n", root, descriptor.Digest)
			if diagnostics.String() != want || *loads != 0 {
				t.Fatalf("discovered publication = %q, loads %d; want %q", diagnostics.String(), *loads, want)
			}
		})
	}
}

func TestWorkspacePushRefusesBeforeConfigOrCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing", []string{"team:v1"}, "--workspace <directory>"},
		{"context", []string{"team:v1", "--context", "unused"}, "--context"},
		{"engine", []string{"team:v1", "--container-engine", "docker"}, "--container-engine"},
		{"no destination", nil, "usage:"},
		{"extra positional", []string{"one", "two", "three"}, "usage:"},
		{"empty override", []string{"team:v1", "--workspace", ""}, "workspace"},
		{"ambiguous override", []string{"directory", "team:v1", "--workspace", "."}, "workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(append([]string{"suite", "push"}, tc.args...), deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v; want %q", err, tc.want)
			}
			entries, statErr := os.ReadDir(state)
			if statErr != nil || len(entries) != 0 || *loads != 0 || out.Len() != 0 {
				t.Fatal("refusal loaded config, prepared state, or emitted a result", statErr)
			}
		})
	}
}

func TestExtractedSuitePushBypassesBrokenWorkspace(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "agentsuite", "testdata", "minimal"))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("broken: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	state := t.TempDir()
	t.Setenv("KMX_HOME", state)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"suite", "push", fixture, "team:v1", "--to-layout", filepath.Join(t.TempDir(), "layout")}, deps); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 || *loads != 0 || diagnostics.Len() != 0 || !strings.Contains(out.String(), "Pushed AgentSuite minimal") {
		t.Fatal("extracted route consulted workspace or changed result", err)
	}
}
