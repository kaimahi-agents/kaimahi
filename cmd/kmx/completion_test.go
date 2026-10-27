package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCobraCompletionIncludesCompleteCommandTree(t *testing.T) {
	var out, errOut bytes.Buffer
	deps := productionDependencies()
	deps.stdout, deps.stderr = &out, &errOut
	root := newRootCommand(&commandState{deps: deps})
	root.SetArgs([]string{"__complete", "cluster", "qu"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"quickstart", ":4"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("completion output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCompletionScriptsUseCobraProtocol(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps := productionDependencies()
			deps.stdout, deps.stderr = &out, &errOut
			root := newRootCommand(&commandState{deps: deps})
			root.SetArgs([]string{"completion", shell})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "__complete") {
				t.Fatalf("%s script lacks Cobra completion protocol", shell)
			}
		})
	}
}

func TestWorkspaceRuntimeCompletionIncludesPinnedDefault(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	if err := execute([]string{"__complete", "create", "--runtime-version", ""}, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v0.10.3", ":4"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("schema completion missing %q: %s", want, out.String())
		}
	}
	if *loads != 0 {
		t.Fatal("schema completion loaded configuration")
	}
}

func TestStaticCompletionIsSortedAndFiltered(t *testing.T) {
	got, directive := staticCompletion([]string{"zeta", "alpha", "beta"})(nil, nil, "b")
	if len(got) != 1 || got[0] != "beta" || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("completion=%v directive=%v", got, directive)
	}
}

func TestContextFlagCompletionAcceptsIncompleteValues(t *testing.T) {
	for _, args := range [][]string{{"__complete", "--context", ""}, {"__complete", "--context=ki"}} {
		var out, errOut bytes.Buffer
		deps := productionDependencies()
		deps.stdout, deps.stderr = &out, &errOut
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(errOut.String(), "needs a context name") {
			t.Fatalf("completion ran operational context validation: %s", errOut.String())
		}
		if !strings.Contains(out.String(), ":4") {
			t.Fatalf("context completion did not disable file completion:\n%s", out.String())
		}
	}
}

func TestCompletionExcludesRetiredApprovalCommands(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		want      []string
		forbidden []string
	}{
		{[]string{"__complete", ""}, []string{"create", "chat", "run", "test", "package", "cluster", ":4"}, []string{"eval\t", "approve", "approvals", "deny", "request", "grants", "audit", "budget", "credential", "flow", "watch"}},
		{[]string{"__complete", "ap"}, []string{":4"}, []string{"approve", "approvals"}},
		{[]string{"__complete", "au"}, []string{":4"}, []string{"audit"}},
	} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(tc.args, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatalf("completion loaded operational configuration: %v", tc.args)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%v completion missing %q:\n%s", tc.args, want, out.String())
			}
		}
		for _, forbidden := range tc.forbidden {
			if strings.Contains(out.String(), forbidden) {
				t.Fatalf("%v completion contains %q:\n%s", tc.args, forbidden, out.String())
			}
		}
	}
}

func TestCompletionContextUsesEngineFlagOverInvalidEnvironment(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("KIND_CLUSTER", "podman-demo")
	t.Setenv("CONTAINER_ENGINE", "containerd")
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	root := newRootCommand(&commandState{deps: deps})
	if err := root.PersistentFlags().Set("container-engine", "podman"); err != nil {
		t.Fatal(err)
	}
	if got := completionContext(root); got != "kind-podman-demo" {
		t.Fatalf("context = %q", got)
	}
}

func TestGuardedWindowsDeferredCompletionIsInert(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl fixture uses a Unix shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\necho kind-sentinel\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, args := range [][]string{
		{"__complete", "--context", ""},
		{"__complete", "cluster", "context", "use", ""},
		{"__complete", "run", "--agent", ""},
		{"__complete", "deploy", "--to", ""},
	} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.goos = "windows"
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if *loads != 0 || strings.Contains(out.String(), "kind-sentinel") {
			t.Fatalf("%v completion had effects: loads=%d stdout=%q stderr=%q", args, *loads, out.String(), errOut.String())
		}
	}
}
