package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestCommandGroupsOffline(t *testing.T) {
	for _, path := range []string{"cluster", "cluster context", "cluster task", "cluster orka", "package", "package suite", "package test --help", "package build --help", "package run-profile --help", "cluster status --help"} {
		t.Run(path, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(strings.Fields(path), deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 || !strings.Contains(out.String(), "Usage:") {
				t.Fatalf("loads=%d output=%q", *loads, out.String())
			}
		})
	}
}

func TestCommandGroupsRemovedRoots(t *testing.T) {
	for _, root := range []string{"local", "context", "ctx", "quickstart", "console", "orka", "status", "push", "deploy", "task", "suite", "targets", "run-profile", "aks", "quickstart-wizard"} {
		for _, args := range [][]string{{root}, {root, "--help"}, {"help", root}} {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err == nil {
				t.Errorf("removed %v accepted: %s", args, out.String())
			}
			if *loads != 0 {
				t.Errorf("%v loaded config", args)
			}
		}
	}
}

func TestCommandGroupsTestBoundaries(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	file := cliPreviewFile(t)
	for _, args := range [][]string{
		{"test", "--workspace", t.TempDir()}, {"test", "--to", "dev"},
		{"package", "test", file}, {"package", "test", "--provider", "fake"},
		{"test", "explain"}, {"test", "explain", "--workspace", t.TempDir()}, {"eval", file},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
		deps.fileRunner = runner
		if err := execute(args, deps); err == nil {
			t.Errorf("mixed/unfinished mode %v accepted", args)
		}
		if *loads != 0 || runner.calls != 0 {
			t.Errorf("%v: config=%d runner=%d", args, *loads, runner.calls)
		}
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	if err := execute([]string{"test", file, "--provider", "fake"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || runner.calls != 1 {
		t.Fatalf("file Test config=%d runner=%d", *loads, runner.calls)
	}
}

func TestCommandGroupsLegacyExplain(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	file := cliPreviewFile(t)
	for _, prefix := range [][]string{{"test"}, {"package", "test"}, {"eval"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		args := append(append([]string{}, prefix...), "explain", "--file", file)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatal("offline Explain loaded configuration")
		}
		if prefix[0] == "eval" && diagnostics.String() != "kmx eval is deprecated; use kmx package test (same arguments).\n" {
			t.Fatalf("notice=%q", diagnostics.String())
		}
	}
}

func TestCommandGroupsRemovedHelpersCannotRunBareFiles(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	for _, name := range []string{"cleanup", "diff", "export", "verify", "from-sessions"} {
		if err := os.WriteFile(filepath.Join(dir, name), source, 0600); err != nil {
			t.Fatal(err)
		}
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
		deps.fileRunner = runner
		if err := execute([]string{"test", name}, deps); err == nil || !strings.Contains(err.Error(), "workspace helper moved") || runner.calls != 0 || *loads != 0 {
			t.Fatalf("%s: %v calls=%d loads=%d", name, err, runner.calls, *loads)
		}
		if err := execute([]string{"test", "./" + name, "--provider", "fake"}, deps); err != nil || runner.calls != 1 {
			t.Fatalf("explicit file: %v calls=%d", err, runner.calls)
		}
	}
}

func TestCommandGroupsNativeHelperHelpUsesCanonicalPaths(t *testing.T) {
	for _, path := range []string{"package test", "package test cleanup"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(append(strings.Fields(path), "--help"), deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || strings.Contains(out.String(), "kmx test") || !strings.Contains(out.String(), "kmx "+path) {
			t.Fatalf("%s: %s", path, out.String())
		}
	}
}
