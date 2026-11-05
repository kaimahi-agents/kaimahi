package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestLocalAgentListIsConfigFreeAndContextRoutesToOrka(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.readLocalAgents = func(all bool) ([]app.LocalAgentInstance, error) {
		if all {
			t.Fatal("unexpected all")
		}
		return []app.LocalAgentInstance{{ID: "md-1234567890abcdef", Name: "greeter", State: "active", Model: "fake"}}, nil
	}
	if err := execute([]string{"list", "-o", "json"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), `"scope":"local"`) || !strings.Contains(out.String(), "greeter") {
		t.Fatalf("local list: loads=%d out=%s", *loads, &out)
	}
	out.Reset()
	deps.loadConfig = func(contextName, engine string) (*config.Config, error) {
		*loads++
		return nil, errors.New("remote selected")
	}
	if err := execute([]string{"--context", "dev", "list"}, deps); err == nil || !strings.Contains(err.Error(), "remote selected") || *loads != 1 {
		t.Fatalf("remote route: err=%v loads=%d", err, *loads)
	}
}

func TestLocalAgentStopUsesLocalLifecycleOnly(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	called := ""
	deps.stopLocalAgent = func(_ context.Context, selector string) error { called = selector; return nil }
	if err := execute([]string{"stop", "greeter"}, deps); err != nil {
		t.Fatal(err)
	}
	if called != "greeter" || *loads != 0 || !strings.Contains(out.String(), "Stopped greeter") {
		t.Fatalf("called=%q loads=%d out=%q", called, *loads, out.String())
	}
}

func TestPersistentRunActivatesCheckedPairAndOrdinaryRunDoesNot(t *testing.T) {
	file := cliPreviewFile(t)
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "persistent"}[persistent], func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			calls := 0
			var activated app.LocalAgentInstance
			deps.activateLocalAgent = func(_ context.Context, executable string, pair *agentfile.Pair, provider, endpoint, model string) (app.LocalAgentInstance, bool, error) {
				calls++
				if pair.AgentPath != file || pair.SnapshotRevision == "" || provider != "fake" || model != "fake" {
					t.Fatalf("pair=%+v provider=%q model=%q", pair, provider, model)
				}
				activated = app.LocalAgentInstance{ID: "md-123", Name: pair.Agent.Identity.ID, AgentRevision: pair.Agent.Revision, SnapshotRevision: pair.SnapshotRevision, TestsState: pair.Tests.State, TestsRevision: pair.Tests.Revision, CachedAgent: pair.AgentPath, Provider: provider, Model: model}
				return activated, true, nil
			}
			deps.readLocalAgent = func(string) (app.LocalAgentInstance, error) {
				if activated.ID == "" {
					return app.LocalAgentInstance{}, os.ErrNotExist
				}
				return activated, nil
			}
			deps.pingLocalAgent = func(context.Context, app.LocalAgentInstance) error { return nil }
			deps.loadLocalAgentPair = func(_ context.Context, instance app.LocalAgentInstance) (*agentfile.Pair, error) {
				return agentfile.LoadPair(instance.CachedAgent, instance.CachedTests)
			}
			deps.withLocalAgentRun = func(ctx context.Context, instance app.LocalAgentInstance, run func(*agentfile.Pair) error) error {
				pair, err := deps.loadLocalAgentPair(ctx, instance)
				if err != nil {
					return err
				}
				return run(pair)
			}
			deps.fileRunner = &cliPreviewFake{}
			args := []string{"run", file, "Ada", "--model", "fake"}
			if persistent {
				args = append(args, "--persist")
			}
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if calls != map[bool]int{false: 0, true: 1}[persistent] || *loads != 0 {
				t.Fatalf("persistent=%v calls=%d loads=%d", persistent, calls, *loads)
			}
		})
	}
}

func TestPersistentRunWithoutInputStartsWithoutModelExecution(t *testing.T) {
	file := cliPreviewFile(t)
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	deps.activateLocalAgent = func(_ context.Context, _ string, pair *agentfile.Pair, _, _, _ string) (app.LocalAgentInstance, bool, error) {
		return app.LocalAgentInstance{ID: "md-123", Name: pair.Agent.Identity.ID, SnapshotRevision: pair.SnapshotRevision}, true, nil
	}
	deps.fileRunner = &cliPreviewFake{cause: errors.New("must not execute")}
	if err := execute([]string{"run", file, "--persist", "--model", "fake"}, deps); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "Started") {
		t.Fatalf("out=%q diagnostics=%q", out.String(), diagnostics.String())
	}
}

func TestLocalLifecycleRejectsNativeWindowsBeforeEffects(t *testing.T) {
	file := cliPreviewFile(t)
	for _, args := range [][]string{{"run", file, "--persist", "--model", "fake"}, {"list"}, {"stop", "greeter"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.goos = "windows"
		if err := execute(args, deps); !errors.Is(err, errWindowsOperationUnsupported) || *loads != 0 || out.Len() != 0 {
			t.Fatalf("args=%v err=%v loads=%d out=%q", args, err, *loads, out.String())
		}
	}
}

func TestFileTestCheckValidatesWithoutExecution(t *testing.T) {
	file := cliPreviewFile(t)
	for _, output := range []string{"text", "json"} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.fileRunner = &cliPreviewFake{cause: errors.New("must not execute")}
		if err := execute([]string{"test", file, "--check", "-o", output}, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || !strings.Contains(out.String(), "sha256:") {
			t.Fatalf("output=%q loads=%d", out.String(), *loads)
		}
	}
}
