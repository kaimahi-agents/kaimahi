package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestCandidateEvaluationAndLiftHelp(t *testing.T) {
	for _, tc := range []struct {
		command string
		terms   []string
	}{
		{"eval", []string{"fresh digest/run-scoped candidates", "verified cleanup", "test cleanup"}},
		{"lift", []string{"requires --promote even in the same environment", "absent canonical resources"}},
	} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		root := newRootCommand(&commandState{deps: deps})
		root.SetArgs([]string{tc.command, "--help"})
		if err := root.Execute(); err != nil || *loads != 0 {
			t.Fatal(err, *loads)
		}
		for _, term := range tc.terms {
			if !strings.Contains(out.String(), term) {
				t.Errorf("%s help lacks %q", tc.command, term)
			}
		}
	}
}

func TestEvalCleanupDoesNotInheritEvaluationPreparation(t *testing.T) {
	var out bytes.Buffer
	deps, loads := testDependencies(&out, &out)
	boundary := errors.New("cleanup explicit boundary")
	deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, boundary }
	root := newRootCommand(&commandState{deps: deps})
	eval, _, err := root.Find([]string{"eval"})
	if err != nil {
		t.Fatal(err)
	}
	eval.PersistentPreRunE = func(*cobra.Command, []string) error { t.Fatal("cleanup inherited evaluation preparation"); return nil }
	root.SetArgs([]string{"eval", "cleanup", "--to-context", "kind-explicit"})
	if err := root.Execute(); !errors.Is(err, boundary) || *loads != 1 {
		t.Fatal(err, *loads)
	}
}

func TestEvalCleanupRoutingAndSelection(t *testing.T) {
	runID := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name    string
		args    []string
		context string
		want    string
	}{
		{"help", []string{"--help"}, "", ""},
		{"explicit", []string{"--to-context", "kind-explicit"}, "kind-explicit", ""},
		{"named", []string{"--to", "dev"}, "kind-saved", ""},
		{"run", []string{runID, "--to", "dev"}, "kind-saved", ""},
		{"named locator", []string{"--to", "dev", "--workspace", "."}, "kind-saved", ""},
		{"runtime mismatch", []string{"--to", "dev"}, "", "Orka-only"},
		{"override", []string{"--to", "dev", "--to-context", "kind-explicit", "--to-namespace", "chosen"}, "kind-explicit", ""},
		{"no fallback", nil, "", "requires --to"},
		{"workspace needs named", []string{"--workspace", ".", "--to-context", "kind-explicit"}, "", "--workspace"},
		{"root context", []string{"--context", "kind-explicit", "--to", "dev"}, "", "--context"},
		{"parent out", []string{"--out", "private.json", "--to", "dev"}, "", "unknown flag"},
		{"parent inference", []string{"--inference", "provider:secret", "--to", "dev"}, "", "unknown flag"},
		{"prefix", []string{"aaa", "--to", "dev"}, "", "full 32"},
		{"uppercase", []string{strings.Repeat("A", 32), "--to", "dev"}, "", "full 32"},
		{"two runs", []string{runID, runID, "--to", "dev"}, "", "usage:"},
		{"wildcard", []string{"*", "--to", "dev"}, "", "full 32"},
		{"new flag refused", []string{"--run-id", runID, "--to", "dev"}, "", "unknown flag"},
		{"empty engine", []string{"--container-engine=", "--to", "dev"}, "", "requires docker or podman"},
		{"irrelevant engine", []string{"--container-engine=docker", "--to", "dev"}, "", "--container-engine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := destinationWorkspace(t, "version: 1\nevalTarget: dev\ntargets: {dev: {context: kind-saved, namespace: saved, runtime: orka, inference: 'provider:missing-private'}}\n")
			t.Chdir(root)
			if tc.name == "runtime mismatch" {
				os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("version: 1\ntargets: {dev: {context: kind-saved, runtime: kagent, bindings: missing-private.yaml}}\n"), 0600)
			}
			// Named cleanup reads no member files; explicit cleanup reads no descriptor.
			if tc.name == "explicit" {
				os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("invalid: ["), 0600)
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("cleanup configured boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != tc.context {
					t.Fatalf("context %q want %q", ctx, tc.context)
				}
				os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("invalid: ["), 0600)
				return nil, boundary
			}
			err := execute(append([]string{"eval", "cleanup"}, tc.args...), deps)
			if tc.context != "" {
				if !errors.Is(err, boundary) || *loads != 1 {
					t.Fatal(err, *loads)
				}
				wantNamespace := `namespace="saved"`
				if tc.name == "override" {
					wantNamespace = `namespace="chosen"`
				}
				if tc.name == "explicit" {
					wantNamespace = `namespace="orka-system"`
				}
				if !strings.Contains(diag.String(), wantNamespace) {
					t.Fatal("namespace not copied", diag.String())
				}
				if strings.Contains(diag.String(), "missing-private") {
					t.Fatal("leaked saved binding")
				}
			} else if tc.name == "help" {
				if err != nil || *loads != 0 || !strings.Contains(out.String(), "cleanup") {
					t.Fatal(err, *loads)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 {
				t.Fatal(err, *loads)
			}
		})
	}
}
