package main

import (
	"bytes"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func testDependencies(out, errOut *bytes.Buffer) (dependencies, *int) {
	loads := 0
	deps := productionDependencies()
	deps.stdout, deps.stderr = out, errOut
	deps.loadConfig = func(context string) (*config.Config, error) {
		loads++
		return &config.Config{KubeContext: context, Credential: "default-cred"}, nil
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

func TestGuardRetryKeepsInvocationArgumentsAndResolvedTarget(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	deps.loadConfig = func(string) (*config.Config, error) {
		return &config.Config{KubeContext: "kind-other", KindCluster: "other", ContainerEngine: "podman", Credential: "finance"}, nil
	}
	state := &commandState{deps: deps, argv: []string{"budget", "a'b; $(bad)", "--cents", "0", "--tokens", "300"}}
	root := newRootCommand(state)
	cmd, _, err := root.Find([]string{"budget"})
	if err != nil {
		t.Fatal(err)
	}
	var invocation string
	cmd.RunE = appRun(state, func(a *app.App) error {
		invocation = a.InvocationCommand
		return nil
	})
	root.SetArgs(state.argv)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "KIND_CLUSTER=other CONTAINER_ENGINE=podman CRED=finance kmx --context kind-other budget 'a'\"'\"'b; $(bad)' --cents 0 --tokens 300"
	if invocation != want {
		t.Fatalf("retry lost target or arguments:\n got: %s\nwant: %s", invocation, want)
	}
}

func TestBareGroupsShowCobraHelpWithoutLoadingConfig(t *testing.T) {
	for _, group := range []string{"agent", "models", "credential"} {
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

// commandPaths walks the Cobra tree and returns every command a user can
// type, as a space-joined path. Deriving the set instead of listing it is the
// point: a hand-written list only ever proves that the commands someone
// remembered still exist.
func commandPaths(root *cobra.Command) []string {
	var paths []string
	var walk func(parent *cobra.Command, prefix string)
	walk = func(parent *cobra.Command, prefix string) {
		for _, child := range parent.Commands() {
			path := strings.TrimSpace(prefix + " " + child.Name())
			paths = append(paths, path)
			walk(child, path)
		}
	}
	walk(root, "")
	sort.Strings(paths)
	return paths
}

// The command tree is the product surface: every path in it is something an
// operator can type, and every one of them needs a help line, a document and,
// where it mutates, a guard. A command that arrives without anyone noticing
// gets none of those. So this list is checked in both directions — each path
// named here must resolve, and each command in the tree must be named here.
// Adding or removing a subcommand fails this test until the list follows.
func TestTheCommandTreeIsExactlyWhatIsListedHere(t *testing.T) {
	want := []string{
		"agent", "agent chat", "agent create", "agent edit", "agent list", "agent show",
		"backup", "budget", "completion",
		"credential", "credential issue", "credential renew", "credentials",
		"ctx", "down", "flow", "govern", "ledger",
		"lift", "lift down", "metrics", "migrate", "models", "models add",
		"models credential", "models credential copilot", "orka", "orka install", "orka status",
		"plane", "quickstart",
		"restore", "status",
		"up", "use", "version", "watch",
	}
	sort.Strings(want)

	root := newRootCommand(&commandState{deps: productionDependencies()})
	if got := commandPaths(root); !reflect.DeepEqual(got, want) {
		t.Errorf("command tree drifted from the list in this test.\n got: %v\nwant: %v", got, want)
	}

	// Resolution is a separate property from membership: a command can be
	// registered under a name the user cannot reach if a parent claims the
	// argument first.
	for _, path := range want {
		fields := strings.Fields(path)
		command, remaining, err := root.Find(fields)
		if err != nil || command == root || len(remaining) != 0 {
			t.Errorf("command %q does not resolve: command=%v remaining=%v err=%v", path, command.Name(), remaining, err)
		}
		if command.RunE == nil {
			t.Errorf("command %q resolves to a command that does nothing", path)
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
		{[]string{"credential", "renew", "demo", "--ttl", "1d"}, []string{"demo"}},
		{[]string{"credential", "issue", "demo", "--discard", "--ttl", "1d"}, []string{"demo"}},
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
	for _, args := range [][]string{{"credential", "frob"}, {"models", "frob"}, {"agent", "frob"}} {
		var out, errOut bytes.Buffer
		deps, _ := testDependencies(&out, &errOut)
		if err := execute(args, deps); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
	}
}

func TestAuditInboundIsRejectedBeforeLoadingConfig(t *testing.T) {
	for _, args := range [][]string{{"audit", "inbound"}, {"audit", "inbound", "demo"}} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.loadConfig = func(string) (*config.Config, error) {
			*loads++
			return nil, errors.New("operational config must not be loaded")
		}
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "unknown command \"audit\"") {
			t.Fatalf("%v must be rejected as a retired audit command: %v", args, err)
		}
		if *loads != 0 {
			t.Fatalf("%v loaded config for a retired audit trail", args)
		}
	}
}

func TestCredentialIssueRequiresExactlyOneDestination(t *testing.T) {
	for _, args := range [][]string{
		{"credential", "issue", "inbound-demo"},
		{"credential", "issue", "inbound-demo", "--discard=false"},
		{"credential", "issue", "inbound-demo", "--discard", "--secret", "inbound-token"},
	} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(args, deps); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
		if *loads != 0 {
			t.Fatalf("%v loaded config before enforcing the destination", args)
		}
	}
}

func TestCredentialIssueSecretDefaultsToKagentNamespace(t *testing.T) {
	root := newRootCommand(&commandState{deps: productionDependencies()})
	issue, _, err := root.Find([]string{"credential", "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if got := issue.Flag("namespace").DefValue; got != config.DefaultNamespace {
		t.Fatalf("--namespace default=%q, want %q", got, config.DefaultNamespace)
	}
}
