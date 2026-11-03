package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func TestLocalAgentIDsCompleteWithoutExternalProbes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Markdown instance leases are Unix-only")
	}
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	t.Setenv("PATH", t.TempDir())
	source := filepath.Join(t.TempDir(), "frog.agent.md")
	id := fmt.Sprintf("md-%x", sha256.Sum256([]byte(source)))
	short := strings.TrimPrefix(id, "md-")[:12]
	dir := filepath.Join(home, "agents", "instances", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "id": id, "name": "frog", "type": "md", "source": source, "version": "sha256:" + strings.Repeat("a", 64), "generation": 1, "state": "running"})
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := os.OpenFile(filepath.Join(dir, "active.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = testMDLease(lease, true); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"stop", "rm", "chat", "inspect", "run", "test"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err = execute([]string{"__complete", command, short[:6]}, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || !strings.Contains(out.String(), short+"\tfrog") {
			t.Fatalf("%s: %s loads=%d", command, &out, *loads)
		}
	}
	if err = testMDLease(lease, false); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	if err = execute([]string{"__complete", "stop", short[:6]}, deps); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), short) {
		t.Fatal("stop completes stopped instance")
	}
	out.Reset()
	if err = execute([]string{"__complete", "rm", short[:6]}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), short) {
		t.Fatal("rm does not complete stopped instance")
	}
	for _, command := range []string{"run", "test"} {
		out.Reset()
		if err = execute([]string{"__complete", command, short[:6]}, deps); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), short) {
			t.Fatalf("%s omitted stopped ID", command)
		}
	}
	for _, command := range []string{"run", "test"} {
		out.Reset()
		if err = execute([]string{"__complete", command, "./missing"}, deps); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), ":0") {
			t.Fatalf("%s did not allow path completion: %s", command, &out)
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
	if err := execute([]string{"__complete", "package", "create", "--runtime-version", ""}, deps); err != nil {
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
