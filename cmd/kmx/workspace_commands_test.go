package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
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
	if !strings.Contains(first, "sha256:") {
		t.Fatalf("publication omitted suite identity: %s", first)
	}
	out.Reset()
	if err := execute([]string{"suite", "push", "--workspace", root, "--to-layout", layout, "team:v1"}, deps); err != nil {
		t.Fatal(err)
	}
	if out.String() != first {
		t.Fatalf("repeat publication changed result: %q -> %q", first, out.String())
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
	for _, args := range [][]string{
		{"agent", "create", "--help"}, {"suite", "push", "--help"},
		{"__complete", "agent", "create", "--workspace", ""},
		{"__complete", "suite", "push", "--workspace", ""},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if *loads != 0 {
			t.Fatal("workspace help/completion loaded operational config")
		}
	}
}
