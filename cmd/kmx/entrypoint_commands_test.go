package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestEntrypointHelpAndCompletionAreNonOperational(t *testing.T) {
	for _, args := range [][]string{
		{"context"}, {"context", "show", "--help"}, {"context", "use", "--help"},
		{"local"}, {"local", "up", "--help"}, {"local", "down", "--help"},
		{"ctx", "--help"}, {"up", "--help"}, {"down", "--help"},
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
			if args[0] == "__complete" && len(args) == 2 {
				for _, name := range []string{"context", "local"} {
					if !strings.Contains(out.String(), name+"\t") {
						t.Fatalf("canonical command missing: %s", out.String())
					}
				}
				for _, line := range strings.Split(out.String(), "\n") {
					name, _, _ := strings.Cut(line, "\t")
					if name == "ctx" || name == "up" || name == "down" {
						t.Fatalf("compatibility route advertised: %s", line)
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
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err == nil || *loads != 0 {
			t.Fatalf("%v: err=%v loads=%d", args, err, *loads)
		}
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
	var outputs []string
	for _, args := range [][]string{{"ctx"}, {"context", "show"}} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		deps.loadConfig = func(_, _ string) (*config.Config, error) {
			return &config.Config{KubeContext: "kind-demo", ContextSource: config.SourceSelected}, nil
		}
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out.String())
		if selected, err := config.ReadSelectedContext(); err != nil || selected != "" {
			t.Fatalf("show changed selection: %q %v", selected, err)
		}
		if strings.Contains(out.String(), "deprecated") || (args[0] == "ctx") != strings.Contains(diagnostics.String(), "deprecated") {
			t.Fatalf("notice routed incorrectly: stdout=%s stderr=%s", &out, &diagnostics)
		}
	}
	if outputs[0] != outputs[1] || !strings.Contains(outputs[1], "kind-demo") {
		t.Fatalf("read output differs: %q", outputs)
	}
	for _, args := range [][]string{{"ctx", "kind-demo"}, {"context", "use", "kind-demo"}} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if selected, err := config.ReadSelectedContext(); err != nil || selected != "kind-demo" {
			t.Fatalf("selection=%q err=%v", selected, err)
		}
	}
	// A refused remote selection cannot replace the saved local context.
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
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

func TestLocalUpCompatibilityPreservesStepAndTargetRefusals(t *testing.T) {
	for _, prefix := range [][]string{{"up"}, {"local", "up"}} {
		for _, step := range []string{"unknown", "cluster"} {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(context, engine string) (*config.Config, error) {
				return &config.Config{KubeContext: context, KindCluster: "demo", ContainerEngine: engine}, nil
			}
			args := append(append([]string(nil), prefix...), "--step", step, "--context", "kind-other", "--container-engine", "podman")
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
			if strings.Contains(out.String(), "deprecated") {
				t.Fatal("notice polluted stdout")
			}
		}
	}
}

func TestLocalDownCompatibilityPreservesClusterAndEngine(t *testing.T) {
	var calls []string
	for _, args := range [][]string{
		{"down", "--context", "kind-demo", "--container-engine", "podman"},
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
			calls = append(calls, string(data))
			if !strings.Contains(string(data), "kind delete cluster --name demo engine=podman") || strings.Contains(string(data), "use-context") {
				t.Fatalf("wrong operation: %s", data)
			}
			if strings.Contains(out.String(), "deprecated") || (args[0] == "down") != strings.Contains(diagnostics.String(), "deprecated") {
				t.Fatalf("notice routed incorrectly: stdout=%s stderr=%s", &out, &diagnostics)
			}
		})
	}
	if len(calls) != 2 || calls[0] != calls[1] {
		t.Fatalf("compatibility changed subprocess calls: %q", calls)
	}
}
