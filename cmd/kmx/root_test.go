package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func testDependencies(out, errOut *bytes.Buffer) (dependencies, *int) {
	loads := 0
	deps := productionDependencies()
	deps.stdout, deps.stderr = out, errOut
	deps.executable = "kmx"
	deps.loadConfig = func(context, engine string) (*config.Config, error) {
		loads++
		return &config.Config{KubeContext: context}, nil
	}
	deps.newApp = func(cfg *config.Config) *app.App { return app.New(cfg) }
	return deps, &loads
}

func TestQuickstartHelpLabelsUnsupportedSuiteFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	if err := execute([]string{"quickstart", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 {
		t.Fatal("help loaded operational configuration")
	}
	for _, flag := range []string{"--skills", "--result-service-account", "--out"} {
		found := false
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, flag+" ") && strings.Contains(line, "unsupported") {
				found = true
			}
		}
		if !found {
			t.Errorf("unsupported flag %s advertised as operational", flag)
		}
	}
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

func TestGuardedWindowsDeferredOperationsRefuseBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--agent", "live", "--prompt", "hello"},
		{"context", "use", "--", "--help"},
		{"test"},
		{"create", "greeter"},
		{"suite", "validate", "missing"},
		{"model", "list"},
		{"local", "up"},
		{"deploy", "suite@sha256:" + strings.Repeat("a", 64)},
		{"mcp", "--root", t.TempDir()},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			deps.goos = "windows"
			home := filepath.Join(t.TempDir(), "state-must-not-exist")
			t.Setenv("KMX_HOME", home)
			err := execute(args, deps)
			if !errors.Is(err, errWindowsOperationUnsupported) {
				t.Fatalf("%v = %v", args, err)
			}
			if *loads != 0 || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("%v had effects: loads=%d stdout=%q stderr=%q", args, *loads, out.String(), errOut.String())
			}
			if _, statErr := os.Lstat(home); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%v created state: %v", args, statErr)
			}
		})
	}
}

func TestGuardedWindowsAllowsPackageFreeFileCommands(t *testing.T) {
	cases := []struct {
		name string
		args func(string) []string
	}{
		{"run", func(file string) []string { return []string{"run", file} }},
		{"run positional input", func(file string) []string { return []string{"run", file, "Grace"} }},
		{"run presentation", func(file string) []string {
			return []string{"run", file, "--case", "greeting", "--output", "json", "--prompt", "hello", "--no-content", "--verbose"}
		}},
		{"test", func(file string) []string { return []string{"test", file} }},
		{"test presentation", func(file string) []string {
			return []string{"test", file, "--case", "greeting", "--output", "json", "--no-content", "--verbose"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			deps.goos = "windows"
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "greeter.agent.md")
			source, err := os.ReadFile(filepath.Join("..", "..", "internal", "kmx", "agentfile", "testdata", "greeter.agent.md"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, source, 0o600); err != nil {
				t.Fatal(err)
			}
			args := tc.args(file)
			err = execute(args, deps)
			if err != nil || *loads != 0 || runner.calls != 1 || out.Len() == 0 {
				t.Fatalf("%v = %v loads=%d sends=%d stdout=%q stderr=%q", args, err, *loads, runner.calls, out.String(), errOut.String())
			}
			if runner.request.Route.Provider != "fake" || runner.request.Route.Model != "fake" || runner.request.Route.Endpoint != "" || runner.request.Route.Auth != "none" || runner.request.ToolBindings != "" || len(runner.request.Agent.Tools) != 0 {
				t.Fatalf("%v escaped package-free fake route: %+v", args, runner.request)
			}
		})
	}
}

func TestGuardedWindowsFileCommandsRefuseEveryExternalRouteBeforeEffects(t *testing.T) {
	file := cliPreviewFile(t)
	toolFile := filepath.Join(filepath.Dir(file), "tool.agent.md")
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	toolSource := bytes.Replace(source, []byte("## Examples"), []byte("## Tools\n- lookup\n## Examples"), 1)
	if err := os.WriteFile(toolFile, toolSource, 0o600); err != nil {
		t.Fatal(err)
	}
	realModelFile := filepath.Join(filepath.Dir(file), "real.agent.md")
	realModelSource := bytes.Replace(source, []byte("**Preferred:** fake"), []byte("**Preferred:** model-v1"), 1)
	if err := os.WriteFile(realModelFile, realModelSource, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"run provider", []string{"run", file, "--provider", "copilot"}},
		{"run endpoint", []string{"run", file, "--provider-url", "https://example.invalid"}},
		{"run tool bindings", []string{"run", file, "--tool-bindings", "bindings.json"}},
		{"run prompt file", []string{"run", file, "--prompt-file", "prompt.txt"}},
		{"run live selector", []string{"run", file, "--agent", "live"}},
		{"run namespace", []string{"run", file, "--namespace", "agents"}},
		{"run wait", []string{"run", file, "--wait", "10s"}},
		{"run result port", []string{"run", file, "--result-port", "1234"}},
		{"run engine", []string{"run", file, "--container-engine", "docker"}},
		{"run context", []string{"run", file, "--context", "kind-live"}},
		{"run tool charter", []string{"run", toolFile}},
		{"run nonfake charter", []string{"run", realModelFile}},
		{"test provider", []string{"test", file, "--provider", "copilot"}},
		{"test endpoint", []string{"test", file, "--provider-url", "https://example.invalid"}},
		{"test tool bindings", []string{"test", file, "--tool-bindings", "bindings.json"}},
		{"test GitHub summary", []string{"test", file, "--output", "json", "--github-summary"}},
		{"test workspace", []string{"test", file, "--workspace", filepath.Dir(file)}},
		{"test destination", []string{"test", file, "--to", "remote"}},
		{"test inference", []string{"test", file, "--inference", "provider:live"}},
		{"test judge endpoint", []string{"test", file, "--judge-base-url", "https://example.invalid"}},
		{"test development host", []string{"test", file, "--development-host", "host.json"}},
		{"test result port", []string{"test", file, "--result-port", "1234"}},
		{"test engine", []string{"test", file, "--container-engine", "docker"}},
		{"test context", []string{"test", file, "--context", "kind-live"}},
		{"test tool charter", []string{"test", toolFile}},
		{"test nonfake charter", []string{"test", realModelFile}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			deps.goos = "windows"
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			home := filepath.Join(t.TempDir(), "state-must-not-exist")
			t.Setenv("KMX_HOME", home)
			summary := filepath.Join(t.TempDir(), "summary-must-not-exist.md")
			t.Setenv("GITHUB_STEP_SUMMARY", summary)
			err := execute(tc.args, deps)
			if !errors.Is(err, errWindowsOperationUnsupported) {
				t.Fatalf("%v = %v", tc.args, err)
			}
			if *loads != 0 || runner.calls != 0 || out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("%v had effects: loads=%d sends=%d stdout=%q stderr=%q", tc.args, *loads, runner.calls, out.String(), errOut.String())
			}
			if _, statErr := os.Lstat(home); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%v created state: %v", tc.args, statErr)
			}
			if _, statErr := os.Lstat(summary); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%v created a GitHub summary: %v", tc.args, statErr)
			}
		})
	}
}

func TestGuardedWindowsFileSnapshotCannotChangeRouteAfterAdmission(t *testing.T) {
	file := cliPreviewFile(t)
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(source, []byte("**Preferred:** fake"), []byte("**Preferred:** model-v1"), 1)
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	deps.goos = "windows"
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	home := filepath.Join(t.TempDir(), "state-must-not-exist")
	t.Setenv("KMX_HOME", home)
	root := newRootCommand(&commandState{deps: deps, argv: []string{"run", file}})
	run, _, err := root.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	run.PreRunE = func(*cobra.Command, []string) error { return os.WriteFile(file, changed, 0o600) }
	root.SetArgs([]string{"run", file})
	err = root.Execute()
	if !errors.Is(err, errWindowsOperationUnsupported) {
		t.Fatalf("changed route = %v", err)
	}
	if *loads != 0 || runner.calls != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("changed route had effects: loads=%d sends=%d stdout=%q stderr=%q", *loads, runner.calls, out.String(), errOut.String())
	}
	if _, statErr := os.Lstat(home); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed route created state: %v", statErr)
	}

	var summaryOut, summaryErrOut bytes.Buffer
	summaryDeps, summaryLoads := testDependencies(&summaryOut, &summaryErrOut)
	summaryDeps.goos = "windows"
	summaryRunner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	summaryDeps.fileRunner = summaryRunner
	summaryHome := filepath.Join(t.TempDir(), "state-must-not-exist")
	summary := filepath.Join(t.TempDir(), "summary-must-not-exist.md")
	t.Setenv("KMX_HOME", summaryHome)
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	summaryFile := cliPreviewFile(t)
	root = newRootCommand(&commandState{deps: summaryDeps, argv: []string{"test", summaryFile, "--output", "json"}})
	testCommand, _, err := root.Find([]string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	testCommand.PreRunE = func(cmd *cobra.Command, _ []string) error {
		return cmd.Flags().Set("github-summary", "true")
	}
	root.SetArgs([]string{"test", summaryFile, "--output", "json"})
	err = root.Execute()
	if !errors.Is(err, errWindowsOperationUnsupported) {
		t.Fatalf("changed summary mode = %v", err)
	}
	if *summaryLoads != 0 || summaryRunner.calls != 0 || summaryOut.Len() != 0 || summaryErrOut.Len() != 0 {
		t.Fatalf("changed summary mode had effects: loads=%d sends=%d stdout=%q stderr=%q", *summaryLoads, summaryRunner.calls, summaryOut.String(), summaryErrOut.String())
	}
	if _, statErr := os.Lstat(summaryHome); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed summary mode created state: %v", statErr)
	}
	if _, statErr := os.Lstat(summary); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed summary mode created a GitHub summary: %v", statErr)
	}
}

func TestGuardedWindowsExplainRequiresExplicitFileAndAllowsFileScopedClear(t *testing.T) {
	file := cliPreviewFile(t)
	allowed := [][]string{
		{"test", "explain", "--file", file},
		{"test", "explain", "f-" + strings.Repeat("a", 32), "--file", file, "--show-content"},
		{"test", "explain", "--file", file, "--clear"},
		{"eval", "explain", "--file", file},
	}
	for _, args := range allowed {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.goos = "windows"
		root := newRootCommand(&commandState{deps: deps})
		cmd, remaining, err := root.Find(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if err := cmd.ParseFlags(remaining); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if err := cmd.Args(cmd, cmd.Flags().Args()); errors.Is(err, errWindowsOperationUnsupported) {
			t.Errorf("%v was guarded: %v", args, err)
		}
		wantDiagnostics := ""
		if args[0] == "eval" {
			wantDiagnostics = legacyEvalNotice
		}
		if *loads != 0 || out.Len() != 0 || errOut.String() != wantDiagnostics {
			t.Fatalf("%v classification had effects: loads=%d stdout=%q stderr=%q", args, *loads, out.String(), errOut.String())
		}
	}

	refused := [][]string{
		{"test", "explain"},
		{"test", "explain", "f-" + strings.Repeat("a", 32)},
		{"test", "explain", "--workspace", filepath.Dir(file)},
		{"test", "explain", "--file", ""},
		{"test", "explain", "--file", file, "--workspace", filepath.Dir(file)},
		{"test", "explain", "--file", file, "--container-engine", "docker"},
		{"test", "explain", "--file", file, "--context", "kind-live"},
		{"eval", "explain", "--workspace", filepath.Dir(file)},
	}
	for _, args := range refused {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.goos = "windows"
		home := filepath.Join(t.TempDir(), "state-must-not-exist")
		t.Setenv("KMX_HOME", home)
		err := execute(args, deps)
		if !errors.Is(err, errWindowsOperationUnsupported) {
			t.Errorf("%v = %v", args, err)
		}
		wantDiagnostics := ""
		if args[0] == "eval" {
			wantDiagnostics = legacyEvalNotice
		}
		if *loads != 0 || out.Len() != 0 || errOut.String() != wantDiagnostics {
			t.Errorf("%v had effects: loads=%d stdout=%q stderr=%q", args, *loads, out.String(), errOut.String())
		}
		if _, statErr := os.Lstat(home); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%v created state: %v", args, statErr)
		}
	}
}

func TestGuardedWindowsFileExplainClearStaysSubjectScoped(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	files := []string{cliPreviewFile(t), cliPreviewFile(t)}
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	deps.goos = "windows"
	deps.fileRunner = &cliPreviewFake{verdict: runtime.EvaluationFail}
	ids := make([]string, len(files))
	for i, file := range files {
		out.Reset()
		errOut.Reset()
		err := execute([]string{"test", file, "--output", "json"}, deps)
		if err == nil || exitCode(err) != 1 {
			t.Fatalf("test %q = %v", file, err)
		}
		var result app.FilePreviewResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 1 {
			t.Fatalf("test %q retention = %+v, %v", file, result.Retention, err)
		}
		ids[i] = result.Retention.Diagnostics[0].ID
	}
	out.Reset()
	errOut.Reset()
	if err := execute([]string{"eval", "explain", ids[1], "--file", files[1]}, deps); err != nil || !strings.Contains(out.String(), ids[1]) || errOut.String() != legacyEvalNotice {
		t.Fatalf("legacy file Explain = %v stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := execute([]string{"test", "explain", "--file", files[0], "--clear"}, deps); err != nil || !strings.Contains(out.String(), "Cleared 1") {
		t.Fatalf("file-scoped clear = %v stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if err := execute([]string{"test", "explain", ids[0], "--file", files[0]}, deps); err == nil {
		t.Fatal("cleared file subject remained readable")
	}
	out.Reset()
	errOut.Reset()
	if err := execute([]string{"test", "explain", ids[1], "--file", files[1]}, deps); err != nil || !strings.Contains(out.String(), ids[1]) {
		t.Fatalf("clear crossed file subjects: %v stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	if *loads != 0 {
		t.Fatalf("file Explain loaded operational config %d times", *loads)
	}
}

func TestGuardedWindowsSupportedCommandFlagsAreExhaustivelyAllowlisted(t *testing.T) {
	file := cliPreviewFile(t)
	cases := []struct {
		path       []string
		args       []string
		allFlags   []string
		allowFlags []string
	}{
		{[]string{"run"}, []string{file}, []string{"agent", "case", "container-engine", "context", "model", "namespace", "no-content", "output", "prompt", "prompt-file", "provider", "provider-url", "result-port", "tool-bindings", "verbose", "wait"}, []string{"case", "no-content", "output", "prompt", "verbose"}},
		{[]string{"test"}, []string{file}, []string{"bindings", "case", "case-timeout", "container-engine", "context", "development-host", "github-summary", "inference", "judge-base-url", "judge-key-env", "model", "no-content", "out", "output", "provider", "provider-url", "result-port", "show-content", "to", "to-context", "to-namespace", "tool-bindings", "verbose", "workspace"}, []string{"case", "no-content", "output", "verbose"}},
		{[]string{"test", "explain"}, nil, []string{"clear", "container-engine", "context", "file", "show-content", "workspace"}, []string{"clear", "file", "show-content"}},
		{[]string{"eval", "explain"}, nil, []string{"clear", "container-engine", "context", "file", "show-content", "workspace"}, []string{"clear", "file", "show-content"}},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		deps, _ := testDependencies(&out, &errOut)
		deps.goos = "windows"
		root := newRootCommand(&commandState{deps: deps})
		cmd, _, err := root.Find(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		_ = cmd.InheritedFlags()
		var names []string
		cmd.Flags().VisitAll(func(flag *pflag.Flag) { names = append(names, flag.Name) })
		if !reflect.DeepEqual(names, tc.allFlags) {
			t.Fatalf("%s flags = %v, want %v", strings.Join(tc.path, " "), names, tc.allFlags)
		}
		allowed := make(map[string]bool, len(tc.allowFlags))
		for _, name := range tc.allowFlags {
			allowed[name] = true
		}
		for _, name := range names {
			t.Run(strings.Join(tc.path, "_")+"_"+name, func(t *testing.T) {
				var out, errOut bytes.Buffer
				deps, _ := testDependencies(&out, &errOut)
				deps.goos = "windows"
				state := &commandState{deps: deps}
				root := newRootCommand(state)
				cmd, _, err := root.Find(tc.path)
				if err != nil {
					t.Fatal(err)
				}
				_ = cmd.InheritedFlags()
				if strings.HasSuffix(strings.Join(tc.path, " "), "explain") {
					if err := cmd.Flags().Set("file", file); err != nil {
						t.Fatal(err)
					}
				}
				flag := cmd.Flags().Lookup(name)
				value := flag.DefValue
				if name == "file" {
					value = file
				}
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatal(err)
				}
				got := windowsCommandAllowed(cmd, tc.args, state)
				if got != allowed[name] {
					t.Errorf("--%s allowed=%v, want %v", name, got, allowed[name])
				}
			})
		}
	}
}

func TestGuardedWindowsVersionHelpAndCompletionRemainInert(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"help"}, {"version"}, {"completion", "bash"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			deps.goos = "windows"
			home := filepath.Join(t.TempDir(), "state-must-not-exist")
			t.Setenv("KMX_HOME", home)
			if err := execute(args, deps); err != nil {
				t.Fatalf("%v = %v", args, err)
			}
			if *loads != 0 || out.Len() == 0 || errOut.Len() != 0 {
				t.Fatalf("%v had effects: loads=%d stdout=%q stderr=%q", args, *loads, out.String(), errOut.String())
			}
			if _, statErr := os.Lstat(home); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%v created state: %v", args, statErr)
			}
		})
	}
}

func TestGuardedWindowsHelpAndCompatibilityNotices(t *testing.T) {
	for _, args := range [][]string{{"create", "--help"}, {"eval"}, {"lift"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			deps.goos = "windows"
			err := execute(args, deps)
			if args[len(args)-1] == "--help" {
				if err != nil || out.Len() == 0 || errOut.Len() != 0 {
					t.Fatalf("help = %v stdout=%q stderr=%q", err, out.String(), errOut.String())
				}
			} else {
				if !errors.Is(err, errWindowsOperationUnsupported) || strings.Count(errOut.String(), "deprecated; use kmx") != 1 || out.Len() != 0 {
					t.Fatalf("compatibility = %v stdout=%q stderr=%q", err, out.String(), errOut.String())
				}
			}
			if *loads != 0 {
				t.Fatalf("%v loaded config", args)
			}
		})
	}
}

func TestGuardedWindowsCommandTreeIsExhaustivelyClassified(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	deps.goos = "windows"
	root := newRootCommand(&commandState{deps: deps})
	allowed := map[string]bool{"version": true, "completion": true}
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, cmd := range parent.Commands() {
			path := strings.TrimPrefix(cmd.CommandPath(), "kmx ")
			if !allowed[path] {
				if err := cmd.Args(cmd, nil); !errors.Is(err, errWindowsOperationUnsupported) {
					t.Errorf("%s is not guarded: %v", path, err)
				}
			}
			walk(cmd)
		}
	}
	walk(root)
	if *loads != 0 || out.Len() != 0 {
		t.Fatalf("classification had effects: loads=%d stdout=%q", *loads, out.String())
	}
}

func TestAKSIsAbsentFromRootHelpAndCompletion(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"completion", "bash"}, {"completion", "zsh"}, {"completion", "fish"},
		{"__complete", ""}, {"__complete", "c"}, {"__complete", "aks"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 {
				t.Fatalf("%v loaded operational config %d times", args, *loads)
			}
			if out.Len() == 0 {
				t.Fatalf("%v produced no output", args)
			}
			if strings.Contains(out.String(), "aks") {
				t.Fatalf("%v advertised aks:\n%s", args, out.String())
			}
			if args[0] == "--help" || args[0] == "__complete" && args[1] != "aks" {
				if !strings.Contains(out.String(), "create") {
					t.Fatalf("%v lost visible create command:\n%s", args, out.String())
				}
			}
		})
	}
}

func TestSuiteValidateDoesNotLoadConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	err := execute([]string{"suite", "validate", t.TempDir()}, deps)
	if err == nil || !strings.Contains(err.Error(), "agentsuite.json") {
		t.Fatalf("validation error = %v", err)
	}
	if *loads != 0 {
		t.Fatalf("suite validation loaded operational config %d times", *loads)
	}
}

func TestInteractiveCommandsExposeVerboseFlag(t *testing.T) {
	for _, path := range [][]string{{"quickstart"}, {"chat"}} {
		var out, errOut bytes.Buffer
		deps, _ := testDependencies(&out, &errOut)
		root := newRootCommand(&commandState{deps: deps})
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		flag := cmd.Flags().Lookup("verbose")
		if flag == nil || flag.DefValue != "false" {
			t.Fatalf("%v missing default-off verbose flag", path)
		}
		if err := cmd.ParseFlags([]string{"--verbose"}); err != nil {
			t.Fatal(err)
		}
		if enabled, _ := cmd.Flags().GetBool("verbose"); !enabled {
			t.Fatalf("%v did not accept --verbose", path)
		}
	}
}

func TestContainerEngineFlagOverridesEnvironmentBeforeAppConstruction(t *testing.T) {
	for _, argv := range [][]string{
		{"--container-engine", "podman", "orka", "status"},
		{"orka", "status", "--container-engine", "podman"},
	} {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, _ := testDependencies(&out, &errOut)
			deps.loadConfig = func(_ string, engine string) (*config.Config, error) {
				cfg := &config.Config{ContainerEngine: "docker", KubeContext: "kind-test"}
				if err := cfg.SetContainerEngine(engine); err != nil {
					return nil, err
				}
				return cfg, nil
			}
			var gotEngine string
			var gotEnv, gotUnset []string
			deps.newApp = func(cfg *config.Config) *app.App {
				gotEngine = cfg.ContainerEngine
				a := app.New(cfg)
				gotEnv = append([]string(nil), a.Run.Env...)
				gotUnset = append([]string(nil), a.Run.Unset...)
				return a
			}
			state := &commandState{deps: deps}
			root := newRootCommand(state)
			cmd, _, err := root.Find([]string{"orka", "status"})
			if err != nil {
				t.Fatal(err)
			}
			cmd.RunE = appRun(state, func(*app.App) error { return nil })
			root.SetArgs(argv)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if gotEngine != "podman" || !reflect.DeepEqual(gotEnv, []string{"KIND_EXPERIMENTAL_PROVIDER=podman"}) || !reflect.DeepEqual(gotUnset, []string{"KIND_EXPERIMENTAL_PROVIDER"}) {
				t.Fatalf("engine=%q env=%v unset=%v", gotEngine, gotEnv, gotUnset)
			}
		})
	}
}

func TestContainerEngineFlagRejectsUnknownEngine(t *testing.T) {
	var out, errOut bytes.Buffer
	deps := productionDependencies()
	deps.stdout, deps.stderr = &out, &errOut
	deps.newApp = func(cfg *config.Config) *app.App { return app.New(cfg) }
	err := execute([]string{"orka", "status", "--container-engine", "containerd"}, deps)
	if err == nil || !strings.Contains(err.Error(), "expected docker or podman") {
		t.Fatalf("error = %v", err)
	}
}

func TestExplicitEmptyContainerEngineIsRefused(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	err := execute([]string{"orka", "status", "--container-engine="}, deps)
	if err == nil || !strings.Contains(err.Error(), "requires docker or podman") {
		t.Fatalf("error = %v", err)
	}
}

func TestNativeCommandRecordsResolvedEngineInInvocation(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	deps.loadConfig = func(_, engine string) (*config.Config, error) {
		return &config.Config{KubeContext: "kind-demo", KindCluster: "demo", ContainerEngine: engine}, nil
	}
	state := &commandState{deps: deps, argv: []string{"orka", "show", "demo", "--namespace", "team", "--container-engine", "podman"}}
	root := newRootCommand(state)
	issue, _, err := root.Find([]string{"orka", "show"})
	if err != nil {
		t.Fatal(err)
	}
	var invocation string
	issue.RunE = func(cmd *cobra.Command, _ []string) error {
		a, err := state.operationApplication(cmd)
		if err == nil {
			invocation = a.InvocationCommand
		}
		return err
	}
	root.SetArgs(state.argv)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(invocation, "CONTAINER_ENGINE=podman") || !strings.Contains(invocation, "--container-engine podman") {
		t.Fatalf("invocation lost selected engine: %s", invocation)
	}
}

func TestCobraRejectsInvalidFlagRelationshipsBeforeApplicationConstruction(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"orka execution modes", []string{"orka", "install", "--no-apply", "--dry-run"}, "none of the others can be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.argv, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
			if *loads != 0 {
				t.Fatalf("invalid flags loaded operational config %d times", *loads)
			}
		})
	}
}

func TestGuardRetryKeepsInvocationArgumentsAndResolvedTarget(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	deps.loadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{KubeContext: "kind-other", KindCluster: "other", ContainerEngine: "podman"}, nil
	}
	state := &commandState{deps: deps, argv: []string{"orka", "show", "a'b; $(bad)", "--namespace", "team"}}
	root := newRootCommand(state)
	cmd, _, err := root.Find([]string{"orka", "show"})
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
	want := "KIND_CLUSTER=other CONTAINER_ENGINE=podman kmx --context kind-other orka show 'a'\"'\"'b; $(bad)' --namespace team"
	if invocation != want {
		t.Fatalf("retry lost target or arguments:\n got: %s\nwant: %s", invocation, want)
	}
}

func TestGeneratedCommandsPreserveInvokingExecutable(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	deps.executable = "./build with space/kmx"
	deps.loadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{KubeContext: "remote", KindCluster: "demo", ContainerEngine: "docker"}, nil
	}
	state := &commandState{deps: deps, argv: []string{"orka", "show", "demo", "--namespace", "team"}}
	root := newRootCommand(state)
	cmd, _, err := root.Find([]string{"orka", "show"})
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
	if !strings.Contains(invocation, "'./build with space/kmx'") || strings.Contains(invocation, " kmx --context") {
		t.Errorf("guard retry changed executable: %s", invocation)
	}
}

func TestInvocationExecutableMakesOnlyPathInvocationsAbsolute(t *testing.T) {
	if got := invocationExecutable("kmx"); got != "kmx" {
		t.Fatalf("installed executable changed: %q", got)
	}
	relative := filepath.Join("build with space", "kmx")
	want, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if got := invocationExecutable(relative); got != want {
		t.Fatalf("path executable = %q, want %q", got, want)
	}
}

func TestBareGroupsShowCobraHelpWithoutLoadingConfig(t *testing.T) {
	for _, group := range []string{"orka", "suite", "task"} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		deps.loadConfig = func(string, string) (*config.Config, error) {
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
// operator can type, including hidden retirement stubs. Active paths need a
// help line, a document and, where they mutate, a guard. A command that
// arrives without anyone noticing gets none of those. So this list is checked
// in both directions — each path named here must resolve, and each command in
// the tree must be named here.
// Adding or removing a subcommand fails this test until the list follows.
func TestTheCommandTreeIsExactlyWhatIsListedHere(t *testing.T) {
	want := []string{
		"test", "test cleanup", "test diff", "test explain", "test export", "test from-sessions", "test verify",
		"create", "chat", "run", "eval", "eval cleanup", "eval diff", "eval explain", "eval export", "eval from-sessions", "eval verify", "push", "deploy", "lift",
		"aks", "aks up", "aks down", "completion",
		"context", "context show", "context use", "ctx", "local", "local up", "local down", "mcp", "model", "model list", "model login", "model logout",
		"orka", "orka install", "orka status", "orka show", "orka list",
		"quickstart", "quickstart-wizard", "run-profile", "run-profile export",
		"status", "suite", "suite build", "suite policy", "suite pull", "suite push", "suite push-image", "suite validate", "console", "targets", "task", "task result",
		"version",
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
		{[]string{"chat", "hello", "who", "--verbose"}, []string{"hello", "who"}},
		{[]string{"orka", "show", "demo", "--namespace", "team"}, []string{"demo"}},
		{[]string{"task", "result", "demo", "--wait", "1m"}, []string{"demo"}},
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
	deps.loadConfig = func(string, string) (*config.Config, error) { return nil, errors.New("bad config") }
	if err := execute([]string{"orka", "status"}, deps); err == nil || err.Error() != "bad config" {
		t.Fatalf("config error=%v", err)
	}
}

func TestGroupedCommandsRejectUnknownVerb(t *testing.T) {
	for _, args := range [][]string{{"suite", "frob"}, {"orka", "frob"}, {"bundle", "frob"}, {"eval", "frob"}} {
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
		deps.loadConfig = func(string, string) (*config.Config, error) {
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

// nonTerminalStdin is a real file, so a mode that needs a terminal refuses it
// the way it would refuse a pipe.
func nonTerminalStdin(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// A flag from the other quickstart mode would otherwise be silently ignored,
// so it is refused before configuration loads or a cluster is touched.
func TestQuickstartRejectsFlagsFromTheOtherModeBeforeConfiguration(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"quickstart", "--interactive", "-o", "json"}, "--output does not apply to kmx quickstart --interactive"},
		{[]string{"quickstart", "--interactive", "--output", "text"}, "--output does not apply to kmx quickstart --interactive"},
		{[]string{"quickstart", "-i", "-o", "json"}, "--output does not apply to kmx quickstart --interactive"},
	}
	for _, name := range quickstartInteractiveOnly {
		value := "x"
		if name == "verbose" {
			value = "true"
		}
		cases = append(cases, struct {
			args []string
			want string
		}{[]string{"quickstart", "--" + name + "=" + value}, "--" + name + " requires --interactive"})
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if *loads != 0 {
				t.Fatalf("mode conflict loaded configuration %d times", *loads)
			}
			if out.Len() != 0 {
				t.Fatalf("mode conflict wrote stdout: %q", out.String())
			}
		})
	}
}

// Each mode reaches its own operation: the guided setup refuses a
// non-terminal by its canonical name, and the fixed run validates its own
// output contract.
func TestQuickstartRoutesEachMode(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"quickstart", "--interactive"}, "kmx quickstart --interactive requires an interactive terminal"},
		{[]string{"quickstart", "--interactive", "--task", "hi", "--verbose", "--tools", "k8s-get-resources"}, "quickstart tool shortcuts are retired"},
		{[]string{"quickstart", "-i"}, "kmx quickstart --interactive requires an interactive terminal"},
		{[]string{"quickstart", "-o", "yaml"}, "unknown --output"},
		{[]string{"quickstart", "--task", "hi", "-o", "yaml"}, "unknown --output"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, _ := testDependencies(&out, &errOut)
			deps.stdin = nonTerminalStdin(t)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestQuickstartWizardIsRetiredToQuickstartInteractive(t *testing.T) {
	for _, args := range [][]string{
		{"quickstart-wizard"},
		{"quickstart-wizard", "--tools", "k8s-get-resources", "--verbose"},
		{"--context", "kind-x", "quickstart-wizard", "--azure-discovery", "sdk"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "kmx quickstart-wizard is retired; use kmx quickstart --interactive") {
				t.Fatalf("error = %v", err)
			}
			if *loads != 0 {
				t.Fatalf("retired command loaded configuration %d times", *loads)
			}
		})
	}
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	if err := execute([]string{"quickstart-wizard", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "use kmx quickstart --interactive") {
		t.Fatalf("help did not name the replacement: %q", out.String())
	}
	root := newRootCommand(&commandState{deps: deps})
	if cmd, _, err := root.Find([]string{"quickstart-wizard"}); err != nil || !cmd.Hidden {
		t.Fatalf("quickstart-wizard must exist but be hidden: %v", err)
	}
}

// Chat is always a session, so it needs no mode flag: it passes the command
// layer and reaches the operation, which here stops at its first local
// dependency. The removed --interactive flag is an unknown flag.
func TestAgentChatIsInteractiveWithoutAModeFlag(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	for _, args := range [][]string{
		{"chat", "hello-world-agent"},
		{"chat", "hello-world-agent", "what", "pods", "run?"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, _ := testDependencies(&out, &errOut)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "kubectl is not on PATH") {
				t.Fatalf("chat did not reach its session preflight: %v", err)
			}
		})
	}
	var out, errOut bytes.Buffer
	deps, loads := testDependencies(&out, &errOut)
	err := execute([]string{"chat", "--interactive", "hello-world-agent"}, deps)
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --interactive") {
		t.Fatalf("removed --interactive was accepted: %v", err)
	}
	if *loads != 0 {
		t.Fatalf("unknown flag loaded configuration %d times", *loads)
	}
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find([]string{"chat"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd.UseLine(), "--interactive") || strings.Contains(cmd.Long, "--interactive") {
		t.Fatalf("chat help still teaches --interactive: %q %q", cmd.UseLine(), cmd.Long)
	}
}
