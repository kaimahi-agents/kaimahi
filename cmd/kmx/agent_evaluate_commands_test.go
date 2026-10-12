package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/spf13/cobra"
)

func TestAgentEvaluateJudgeFlags(t *testing.T) {
	for _, name := range []string{"judge-base-url", "judge-key-env"} {
		out := &bytes.Buffer{}
		cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: out}})
		cmd.SetOut(out)
		cmd.SetErr(out)
		cmd.SetArgs([]string{"unused", "--" + name, ""})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--"+name+" requires a non-empty") {
			t.Fatalf("explicit empty accepted: %v", err)
		}
		if cmd.Flags().Lookup("judge-model") != nil {
			t.Fatal("authored model overridden by CLI")
		}
		completion, ok := cmd.GetFlagCompletionFunc(name)
		if !ok {
			t.Fatal("judge flag would fall back to file completion")
		}
		values, directive := completion(cmd, nil, "")
		if len(values) != 0 || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
			t.Fatalf("unsafe completion: %v %v", values, directive)
		}
	}
	cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: &bytes.Buffer{}}})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"authored", "temperature 0", "both", "reasoning", "never"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("help missing %s: %s", text, out)
		}
	}
}

func TestAgentEvaluateSessionsFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"empty target", []string{"--sessions", " "}, "--sessions requires a non-empty"},
		{"empty CA", []string{"--sessions-ca", " "}, "--sessions-ca requires a non-empty"},
		{"context conflict", []string{"--sessions", "127.0.0.1:8080", "--to-context", "test"}, "--sessions cannot be combined with --to-context"},
		{"port conflict", []string{"--sessions", "127.0.0.1:8080", "--result-port", "19180"}, "--result-port is only for Orka"},
		{"CA without target", []string{"--sessions-ca", "ca.pem"}, "--sessions-ca requires --sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: out}})
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetArgs(append([]string{"unused-bundle"}, tc.args...))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAgentEvaluateSessionsBypassesClusterConfiguration(t *testing.T) {
	for _, sessions := range []bool{true, false} {
		t.Run(map[bool]string{true: "sessions", false: "Orka"}[sessions], func(t *testing.T) {
			out := &bytes.Buffer{}
			deps := productionDependencies()
			deps.stdout, deps.stderr = out, out
			calls := 0
			configErr := errors.New("unreadable cluster context configuration")
			deps.loadConfig = func(string, string) (*config.Config, error) {
				calls++
				return nil, configErr
			}
			args := []string{"bundle", "evaluate", filepath.Join(t.TempDir(), "absent")}
			if sessions {
				args = append(args, "--sessions", "127.0.0.1:8080")
			}
			err := execute(args, deps)
			if sessions {
				if calls != 0 || err == nil || !strings.Contains(err.Error(), "read bundle directory") {
					t.Fatalf("sessions required cluster config: calls=%d err=%v", calls, err)
				}
			} else if calls != 1 || !errors.Is(err, configErr) {
				t.Fatalf("Orka no longer uses its config: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAgentEvaluateKagentSelectors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--runtime", "kagent"}, "--runtime-version v0.10.3"},
		{[]string{"--runtime", "kagent", "--runtime-version", "v0.10.2"}, "--runtime-version v0.10.3"},
		{[]string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--sessions", "127.0.0.1:8080"}, "cannot be combined"},
		{[]string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--result-port", "19180"}, "--result-port is only for Orka"},
		{[]string{"--runtime", "kagent", "--runtime-version", ""}, "--runtime-version requires a non-empty"},
		{[]string{"--runtime", "kagent", "--bindings", ""}, "--bindings requires a non-empty"},
		{[]string{"--runtime-version", "v0.10.3"}, "require explicit --runtime kagent"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out := &bytes.Buffer{}
			cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: out}})
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetArgs(append([]string{"unused-bundle"}, tc.args...))
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBundleEvaluateKagentFlagsThroughRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		want  string
		loads int
	}{
		{"qualified Go evaluation", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--bindings", "owner's bindings.yaml", "--to-namespace", "agents", "--to-context", "target-context", "--case", "one", "--case-timeout", "1m", "--judge-base-url", "https://judge.example/v1", "--judge-key-env", "JUDGE_KEY"}, "", 1},
		{"default Orka", nil, "", 1},
		{"missing version", []string{"--runtime", "kagent"}, "kagent evaluation requires explicit --runtime-version v0.10.3", 0},
		{"wrong version", []string{"--runtime", "kagent", "--runtime-version", "v0.10.2"}, "kagent evaluation requires explicit --runtime-version v0.10.3", 0},
		{"version without kagent", []string{"--runtime-version", "v0.10.3"}, "require explicit --runtime kagent", 0},
		{"bindings without kagent", []string{"--bindings", "bindings.yaml"}, "require explicit --runtime kagent", 0},
		{"namespace without kagent", []string{"--to-namespace", "agents"}, "require explicit --runtime kagent", 0},
		{"Orka mode conflict", []string{"--runtime", "orka", "--runtime-version", "v0.10.3"}, "require explicit --runtime kagent", 0},
		{"sessions runtime conflict", []string{"--runtime", "orka", "--sessions", "127.0.0.1:8080"}, "--sessions cannot be combined with --runtime", 0},
		{"kagent sessions conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--sessions", "127.0.0.1:8080"}, "kagent evaluation cannot be combined with sessions or recorded-session options", 0},
		{"kagent CA conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--sessions-ca", "ca.pem"}, "kagent evaluation cannot be combined with sessions or recorded-session options", 0},
		{"kagent reference conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--from-sessions", "manifest.json", "--source-sessions", "127.0.0.1:8080"}, "kagent evaluation cannot be combined with sessions or recorded-session options", 0},
		{"kagent source CA conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--source-sessions-ca", "ca.pem"}, "kagent evaluation cannot be combined with sessions or recorded-session options", 0},
		{"kagent content conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--show-content=false"}, "kagent evaluation cannot be combined with sessions or recorded-session options", 0},
		{"kagent port conflict", []string{"--runtime", "kagent", "--runtime-version", "v0.10.3", "--result-port", "19180"}, "--result-port is only for Orka evaluation", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			poison := errors.New("poisoned configuration boundary")
			deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, poison }
			args := append([]string{"bundle", "evaluate", "unused-bundle"}, tc.args...)
			err := execute(args, deps)
			if *loads != tc.loads || err == nil {
				t.Fatalf("loads=%d want=%d err=%v", *loads, tc.loads, err)
			}
			if tc.loads == 1 {
				if !errors.Is(err, poison) {
					t.Fatalf("new route/flags never reached configuration boundary: %v", err)
				}
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q before configuration, got %v", tc.want, err)
			}
		})
	}
	for _, name := range []string{"runtime", "runtime-version", "bindings", "to-namespace", "to-context"} {
		t.Run("empty "+name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(string, string) (*config.Config, error) {
				*loads++
				return nil, errors.New("poisoned configuration boundary")
			}
			err := execute([]string{"bundle", "evaluate", "unused-bundle", "--" + name, " "}, deps)
			if *loads != 0 || err == nil || !strings.Contains(err.Error(), "--"+name+" requires a non-empty value") {
				t.Fatalf("empty --%s reached configuration: loads=%d err=%v", name, *loads, err)
			}
		})
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"agent", "evaluate", "unused-bundle", "--runtime", "kagent", "--runtime-version", "v0.10.3"}, deps)
	if *loads != 0 || err == nil || !strings.Contains(err.Error(), `unknown command "agent"`) {
		t.Fatalf("removed agent evaluate route restored: loads=%d err=%v", *loads, err)
	}
}

func TestAgentEvaluateSessionsDoesNotUseDefaultOrkaPort(t *testing.T) {
	cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: &bytes.Buffer{}}})
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "absent"), "--sessions", "127.0.0.1:8080"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "read bundle directory") {
		t.Fatalf("sessions command never reached source loading: %v", err)
	}
}
