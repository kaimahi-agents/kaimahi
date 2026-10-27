package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestSessionsAgentDeployArgumentUsage(t *testing.T) {
	for _, verb := range []string{"package deploy", "lift"} {
		t.Run(verb, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(append(strings.Fields(verb), "unexpected", "extra"), deps)
			if err == nil || *loads != 0 {
				t.Fatalf("argument refusal: %v, config loads=%d", err, *loads)
			}
			for _, want := range []string{
				"kmx " + verb + " <ref>@<digest> --to <AgentSessions-destination> --evidence FILE [--name NAME]",
				"(--inference provider:NAME | --bindings FILE)",
				"--harness-image IMAGE --bindings FILE --evidence FILE --plan",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("argument usage %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestSessionsAgentDeploySelectors(t *testing.T) {
	root := destinationWorkspace(t, "version: 1\ntargets:\n  sessions: {context: kind-saved, namespace: agents, runtime: agentsessions}\n")
	for _, tc := range []struct {
		flags []string
		loads int
	}{
		{nil, 1}, {[]string{"--plan", "--name", "new-agent"}, 1}, {[]string{"--update"}, 0}, {[]string{"--promote"}, 0}, {[]string{"--name", "INVALID"}, 0}, {[]string{"--name="}, 0}, {[]string{"--bindings", "missing.yaml"}, 0}, {[]string{"--inference", "provider:x"}, 0},
	} {
		t.Run(strings.Join(tc.flags, "_"), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("config boundary")
			deps.loadConfig = func(ctx, _ string) (*config.Config, error) {
				*loads++
				if ctx != "kind-saved" {
					t.Fatalf("context %s", ctx)
				}
				return nil, boundary
			}
			t.Chdir(root)
			args := []string{"package", "deploy", "registry.example/suite@sha256:" + strings.Repeat("a", 64), "--to", "sessions", "--evidence", "private.json"}
			err := execute(append(args, tc.flags...), deps)
			if err == nil || *loads != tc.loads || tc.loads == 1 && !errors.Is(err, boundary) {
				t.Fatalf("err=%v loads=%d", err, *loads)
			}
		})
	}
}
