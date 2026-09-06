package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func testDependencies(out, errOut *bytes.Buffer) (dependencies, *int) {
	loads := 0
	deps := productionDependencies()
	deps.stdout, deps.stderr = out, errOut
	deps.loadConfig = func(context string) (*config.Config, error) {
		loads++
		return &config.Config{KubeContext: context, Credential: "default-cred", ToolsCredential: "tools-cred"}, nil
	}
	deps.newApp = func(cfg *config.Config) *app.App { return app.New(cfg) }
	return deps, &loads
}

func TestHelpVersionCompletionDoNotLoadConfig(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"help"}, {"version"}, {"completion", "bash"}} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if *loads != 0 {
			t.Fatalf("%v loaded operational config %d times", args, *loads)
		}
	}
}

func TestBareGroupsShowCobraHelpWithoutLoadingConfig(t *testing.T) {
	for _, group := range []string{"agent", "tools", "credential"} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.loadConfig = func(string) (*config.Config, error) {
			*loads++
			return nil, errors.New("bad config")
		}
		if err := execute([]string{group}, deps); err != nil {
			t.Fatalf("%s: %v", group, err)
		}
		if *loads != 0 {
			t.Fatalf("%s loaded config %d times", group, *loads)
		}
		if !strings.Contains(out.String(), "Available Commands:") || !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("%s did not show Cobra help:\n%s", group, out.String())
		}
	}
}

func TestCobraCommandTreeContainsEveryPublicCommand(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	paths := [][]string{
		{"quickstart"},
		{"ctx"}, {"up"}, {"lift"}, {"lift", "down"}, {"agent", "list"}, {"agent", "create"}, {"agent", "edit"}, {"agent", "chat"},
		{"plane"}, {"govern"}, {"credentials"}, {"credential", "renew"}, {"ledger"}, {"grants"}, {"audit"},
		{"use"}, {"budget"}, {"approvals"}, {"approve"}, {"deny"}, {"request"},
		{"tools", "add"}, {"tools", "govern"}, {"tools", "ungovern"}, {"tools", "allow"}, {"tools", "allowlist"},
		{"tools", "sandbox"}, {"tools", "sandbox", "status"},
		{"backup"}, {"restore"}, {"metrics"}, {"status"}, {"down"}, {"completion"}, {"version"},
	}
	for _, path := range paths {
		command, remaining, err := root.Find(path)
		if err != nil || command == root || len(remaining) != 0 {
			t.Errorf("command %v missing: command=%v remaining=%v err=%v", path, command.Name(), remaining, err)
		}
	}
}

func TestContextPreprocessingStillWorksAnywhere(t *testing.T) {
	for _, args := range [][]string{{"--context", "kind-x", "status", "--help"}, {"status", "--help", "-context=kind-x"}} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatalf("help loaded config for %v", args)
		}
	}
}

func TestInterspersedFlagsAreOwnedByCobra(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	for _, tc := range []struct {
		path []string
		want []string
	}{
		{[]string{"agent", "chat", "hello", "who", "--json"}, []string{"hello", "who"}},
		{[]string{"budget", "demo", "--tokens", "1"}, []string{"demo"}},
		{[]string{"approve", "abc", "--uses", "1"}, []string{"abc"}},
	} {
		cmd, args, err := root.Find(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("%v: %v", tc.path, err)
		}
		if got := cmd.Flags().Args(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v positional=%v want %v", tc.path, got, tc.want)
		}
	}
}

func TestRepeatedToolFlagPreservesValuesAndCommas(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	cmd, args, err := root.Find([]string{"tools", "add", "demo", "--url", "http://demo.ns:8080/mcp", "--tool", "get:a,b", "--tool", "post:"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	got, err := cmd.Flags().GetStringArray("tool")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"get:a,b", "post:"}) {
		t.Fatalf("repeated tools=%v", got)
	}
}

func TestErrorsAreReturnedWithoutAutomaticUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	err := execute([]string{"status", "extra"}, deps)
	if err == nil || !strings.Contains(err.Error(), "unknown command") && !strings.Contains(err.Error(), "accepts 0 arg") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("Cobra printed usage for an execution error:\n%s", errOut.String())
	}
}

func TestConfigLoadFailureIsReturnedOnce(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	deps.loadConfig = func(string) (*config.Config, error) { return nil, errors.New("bad config") }
	if err := execute([]string{"status"}, deps); err == nil || err.Error() != "bad config" {
		t.Fatalf("config error=%v", err)
	}
}

func TestGroupedCommandsRejectUnknownVerb(t *testing.T) {
	for _, args := range [][]string{{"credential", "frob"}, {"tools", "frob"}, {"agent", "frob"}} {
		var out, errOut bytes.Buffer
		deps, _ := testDependencies(&out, &errOut)
		if err := execute(args, deps); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
	}
}

// The sandbox installs a privileged DaemonSet, so what it will and will not
// accept is a safety property, not a style one. Cobra enforces all three now,
// but the contract is pinned here so a later refactor cannot loosen it
// silently.
func TestToolsSandboxRefusesWhatItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		// A stray word must not be shrugged off into an install.
		{"a trailing word", []string{"tools", "sandbox", "unexpected"}},
		{"a word after status", []string{"tools", "sandbox", "status", "extra"}},
		// The sandbox is node-level: installed once per cluster, and every
		// governed tool call lands on it whatever credential made it. A
		// --credential accepted and then ignored is how an operator comes
		// to believe they scoped something they did not.
		{"a credential it cannot scope to", []string{"tools", "sandbox", "--credential", "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, _ := testDependencies(&out, &errOut)
			if err := execute(tc.argv, deps); err == nil {
				t.Fatalf("%v was accepted; it must be refused", tc.argv)
			}
		})
	}
}

// The two spellings that must keep working.
func TestToolsSandboxResolvesInstallAndStatus(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	for _, path := range [][]string{{"tools", "sandbox"}, {"tools", "sandbox", "status"}} {
		command, remaining, err := root.Find(path)
		if err != nil || len(remaining) != 0 {
			t.Errorf("%v did not resolve: remaining=%v err=%v", path, remaining, err)
		}
		if command.RunE == nil {
			t.Errorf("%v resolved to a command that does nothing", path)
		}
	}
}
