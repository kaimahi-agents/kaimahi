package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestReleaseLiftHelpAndCompletionAreInert(t *testing.T) {
	for _, args := range [][]string{{"lift", "--help"}, {"help", "lift"}, {"__complete", "lift", ""}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 {
			t.Fatal("help loaded configuration")
		}
		if args[0] != "__complete" && !strings.Contains(out.String(), "--evidence") {
			t.Fatal("missing suite lift contract")
		}
	}
}

func TestReleaseLiftUpdateFlagReachesOperation(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	ref := "repo/suite@sha256:" + strings.Repeat("a", 64)
	err := execute([]string{"lift", ref, "--to-context", "target", "--inference", "provider:inference", "--evidence", "private.json", "--update", "--plan"}, deps)
	if err == nil || *loads != 1 || strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("update flag did not reach GET-only planning: err=%v loads=%d", err, *loads)
	}
}

func TestReleaseLiftUpdateKagentRefusesOffline(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"lift", "repo/suite@sha256:" + strings.Repeat("a", 64), "--to-context", "target", "--bindings", "bindings.yaml", "--evidence", "private.json", "--update"}, deps)
	if err == nil || *loads != 0 || !strings.Contains(err.Error(), "Orka") {
		t.Fatalf("unsupported update reached config: err=%v loads=%d", err, *loads)
	}
}

func TestReleaseLiftUpdateHelpIsInert(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"lift", "--help"}, deps); err != nil || *loads != 0 || !strings.Contains(out.String(), "--update") {
		t.Fatal("public update help missing or operational", err)
	}
}

func TestReleaseLiftKagentSelectorReachesOperation(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	ref := "repo/suite@sha256:" + strings.Repeat("a", 64)
	err := execute([]string{"lift", ref, "--to-context", "target", "--bindings", "bindings.yaml", "--evidence", "private.json", "--plan"}, deps)
	if err == nil || *loads != 1 {
		t.Fatalf("valid kagent selector refused offline: err=%v loads=%d", err, *loads)
	}
}

func TestReleaseLiftSelectorXORBeforeConfiguration(t *testing.T) {
	for _, selectors := range [][]string{nil, {"--inference", "provider:inference", "--bindings", "bindings.yaml"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.newApp = func(*config.Config) *app.App {
			t.Fatal("selector refusal constructed an operational App")
			return nil
		}
		args := []string{"lift", "repo/suite@sha256:" + strings.Repeat("a", 64), "--to-context", "target", "--evidence", "private.json"}
		err := execute(append(args, selectors...), deps)
		if !errors.Is(err, app.ErrReleaseLift) || *loads != 0 {
			t.Fatalf("selectors %v: err=%v loads=%d", selectors, err, *loads)
		}
	}
}

func TestReleaseLiftRejectsBeforeConfiguration(t *testing.T) {
	ref := "repo/suite@sha256:" + strings.Repeat("a", 64)
	base := []string{"lift", ref, "--to-context", "target", "--inference", "provider:inference", "--evidence", "private.json"}
	for _, args := range [][]string{
		{"lift"}, {"lift", "down"}, {"lift", "repo:tag"},
		append(append([]string(nil), base...), "--runtime", "kagent"),
		append(append([]string(nil), base...), "--model", "other"),
		append(append([]string(nil), base...), "--override-gate", "why"),
		append(append([]string(nil), base...), "--workspace", "."),
		append(append([]string(nil), base...), "--output", "yaml"),
		append(append([]string(nil), base...), "--from-layout", ".", "--plain-http"),
		append(append([]string(nil), base...), "--context", "other"),
		append(append([]string(nil), base...), "--to-context", ""),
		append(append([]string(nil), base...), "--evidence", ""),
		append(append([]string(nil), base...), "--inference", "copilot"),
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err == nil || *loads != 0 {
			t.Fatalf("%v: err=%v loads=%d", args, err, *loads)
		}
	}
}
