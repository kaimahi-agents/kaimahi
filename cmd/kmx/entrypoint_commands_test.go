package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestRemovedRootAliasesRejectBeforeConfiguration(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	for _, path := range [][]string{{"up"}, {"down"}} {
		for _, suffix := range [][]string{nil, {"--help"}, {"--unknown-flag"}} {
			args := append(append([]string(nil), path...), suffix...)
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps, loads := testDependencies(&out, &diagnostics)
				err := execute(args, deps)
				if err == nil || !strings.Contains(err.Error(), "unknown command") || *loads != 0 {
					t.Fatalf("%v: err=%v loads=%d stdout=%s stderr=%s", args, err, *loads, &out, &diagnostics)
				}
			})
		}
	}
}

func TestEntrypointHelpAndCompletionAreNonOperational(t *testing.T) {
	for _, args := range [][]string{
		{"context"}, {"context", "show", "--help"}, {"context", "use", "--help"}, {"ctx", "--help"}, {"__complete", "ctx", ""},
		{"local"}, {"local", "up", "--help"}, {"local", "down", "--help"},
		{"--help"}, {"aks", "up", "--help"}, {"aks", "down", "--help"},
		{"__complete", ""}, {"__complete", "context", ""}, {"__complete", "local", ""},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 {
				t.Fatal("help/completion loaded operational configuration")
			}
			if args[0] == "--help" {
				for _, line := range strings.Split(out.String(), "\n") {
					fields := strings.Fields(line)
					if len(fields) > 1 && (fields[0] == "up" || fields[0] == "down") {
						t.Fatalf("removed root route advertised: %s", line)
					}
				}
			}
			if args[0] == "__complete" && len(args) == 2 {
				for _, name := range []string{"context", "local", "ctx"} {
					if !strings.Contains(out.String(), name+"\t") {
						t.Fatalf("canonical command missing: %s", out.String())
					}
				}
				for _, line := range strings.Split(out.String(), "\n") {
					name, _, _ := strings.Cut(line, "\t")
					if name == "up" || name == "down" {
						t.Fatalf("removed root route advertised: %s", line)
					}
				}
			}
		})
	}
}

func TestEntrypointsRejectInvalidArgumentsBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"context", "show", "extra"}, {"context", "use"}, {"context", "use", ""},
		{"context", "use", " "}, {"context", "use", "one", "two"}, {"context", "unknown"},
		{"local", "unknown"}, {"local", "up", "extra"}, {"local", "down", "extra"},
		// ctx is deliberately list-only, not the removed switching alias.
		{"ctx", "kind-demo"}, {"ctx", "use", "prod"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err == nil || *loads != 0 {
			t.Fatalf("%v: err=%v loads=%d", args, err, *loads)
		}
	}
}

func TestQuickstartHelpDistinguishesAnswerFromGuidedReadiness(t *testing.T) {
	for _, args := range [][]string{{"quickstart", "--help"}, {"quickstart", "--interactive", "--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{
				"Fixed mode", "hello-world-agent", "prints its answer",
				"Guided mode (--interactive, experimental)",
				"You may finish at Agent readiness without a model answer",
				"The fixed-mode default question is not submitted in guided mode",
				"not run for an existing Agent",
				"deployed Provider, not host-backed chat inference",
				"kmx quickstart -o json", "kmx quickstart --interactive",
			} {
				if !strings.Contains(out.String(), text) {
					t.Errorf("help lacks %q", text)
				}
			}
			if *loads != 0 {
				t.Fatal("help loaded config")
			}
		})
	}
}

func entrypointTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("KMX_ENTRYPOINT_LOG", log)
	t.Setenv("KMX_HOME", filepath.Join(dir, "state"))
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("PATH", dir)
	tools := map[string]string{
		"kubectl": `printf 'kubectl %s\n' "$*" >> "$KMX_ENTRYPOINT_LOG"
case "$*" in
  'config view -o json') printf '%s' '{"current-context":"unrelated","clusters":[{"name":"c","cluster":{"server":"https://127.0.0.1:6443"}},{"name":"remote","cluster":{"server":"https://example.invalid"}}],"contexts":[{"name":"kind-demo","context":{"cluster":"c"}},{"name":"remote team","context":{"cluster":"remote"}}]}' ;;
esac
`,
		"kind": `printf 'kind %s engine=%s\n' "$*" "$KIND_EXPERIMENTAL_PROVIDER" >> "$KMX_ENTRYPOINT_LOG"
case "$*" in
  version) printf 'kind v0.27.0\n' ;;
  'get clusters') printf 'demo\n' ;;
esac
`,
		"podman": `printf 'podman %s\n' "$*" >> "$KMX_ENTRYPOINT_LOG"`,
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func TestContextCommandsPreserveSelectionAndReadOnlyOutput(t *testing.T) {
	entrypointTools(t)
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	deps.loadConfig = func(_, _ string) (*config.Config, error) {
		return &config.Config{KubeContext: "kind-demo", ContextSource: config.SourceSelected}, nil
	}
	if err := execute([]string{"context", "show"}, deps); err != nil {
		t.Fatal(err)
	}
	if selected, err := config.ReadSelectedContext(); err != nil || selected != "" {
		t.Fatalf("show changed selection: %q %v", selected, err)
	}
	if !strings.Contains(out.String(), "kind-demo") {
		t.Fatalf("show omitted selected context: %s", &out)
	}
	if strings.Contains(out.String(), "deprecated") || strings.Contains(diagnostics.String(), "deprecated") {
		t.Fatalf("canonical command emitted deprecation: stdout=%s stderr=%s", &out, &diagnostics)
	}
	deps, _ = testDependencies(&out, &diagnostics)
	if err := execute([]string{"context", "use", "kind-demo"}, deps); err != nil {
		t.Fatal(err)
	}
	if selected, err := config.ReadSelectedContext(); err != nil || selected != "kind-demo" {
		t.Fatalf("selection=%q err=%v", selected, err)
	}
	// A refused remote selection cannot replace the saved local context.
	deps, _ = testDependencies(&out, &diagnostics)
	deps.stdin = nil
	err := execute([]string{"context", "use", "remote team"}, deps)
	if err == nil {
		t.Fatal("remote context selected without named consent")
	}
	if selected, _ := config.ReadSelectedContext(); selected != "kind-demo" {
		t.Fatalf("refusal changed selection to %q", selected)
	}
	if !strings.Contains(err.Error(), "kmx context use 'remote team'") {
		t.Fatalf("recovery does not quote the selected context: %v", err)
	}
}

func TestLocalUpPreservesStepAndTargetRefusals(t *testing.T) {
	for _, step := range []string{"unknown", "cluster"} {
		t.Run(step, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(context, engine string) (*config.Config, error) {
				return &config.Config{KubeContext: context, KindCluster: "demo", ContainerEngine: engine}, nil
			}
			args := []string{"local", "up", "--step", step, "--context", "kind-other", "--container-engine", "podman"}
			err := execute(args, deps)
			if err == nil {
				t.Fatalf("%v accepted invalid setup", args)
			}
			if step == "unknown" && !strings.Contains(err.Error(), "unknown step") {
				t.Fatalf("step not forwarded: %v", err)
			}
			if step == "cluster" && (!strings.Contains(err.Error(), "kind-other") || !strings.Contains(err.Error(), "kind-demo")) {
				t.Fatalf("target mismatch lost: %v", err)
			}
			if strings.Contains(out.String(), "deprecated") || strings.Contains(diagnostics.String(), "deprecated") {
				t.Fatal("canonical command emitted deprecation")
			}
		})
	}
}

func TestLocalDownPreservesClusterAndEngine(t *testing.T) {
	for _, args := range [][]string{
		{"local", "down", "--context", "kind-demo", "--container-engine", "podman"},
		{"--context", "kind-demo", "local", "down", "--container-engine", "podman"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			log := entrypointTools(t)
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(context, engine string) (*config.Config, error) {
				return &config.Config{KubeContext: context, ContextSource: config.SourceFlag, KindCluster: "demo", ContainerEngine: engine}, nil
			}
			if err := execute(args, deps); err != nil {
				t.Fatalf("%v\n%s", err, &diagnostics)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "kind delete cluster --name demo engine=podman") || strings.Contains(string(data), "use-context") {
				t.Fatalf("wrong operation: %s", data)
			}
			if strings.Contains(out.String(), "deprecated") || strings.Contains(diagnostics.String(), "deprecated") {
				t.Fatalf("canonical command emitted deprecation: stdout=%s stderr=%s", &out, &diagnostics)
			}
		})
	}
}
