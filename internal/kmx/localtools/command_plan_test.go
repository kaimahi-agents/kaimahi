//go:build !windows

package localtools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func TestFreezeCommandPlanIsCredentialFreeAndExact(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.agent.md")
	if err := os.WriteFile(file, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(extra string) {
		t.Helper()
		raw := "tools: {check: Enabled}\ncommandTools:\n  check:\n    command: /bin/sh\n    args: [-c, 'printf ok']\n" + extra
		if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	plan, err := FreezeCommandPlan(t.Context(), file, []string{"check"})
	if err != nil || len(plan.Commands) != 1 || plan.Commands[0].Command != "/bin/sh" || !strings.HasPrefix(plan.ConfigRevision, "sha256:") || !strings.HasPrefix(plan.Revision, "sha256:") || plan.Commands[0].ExecutableDigest == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := OpenCommandPlan(plan); err != nil {
		t.Fatal(err)
	}
	write("    env: {TOKEN: RESIDENT_TOOL_TEST_TOKEN}\n")
	t.Setenv("RESIDENT_TOOL_TEST_TOKEN", "not-transferred")
	if _, err := FreezeCommandPlan(t.Context(), file, []string{"check"}); err == nil {
		t.Fatal("resident plan accepted an environment reference")
	}
}

func TestFreezeCommandPlanPreservesCancellation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.agent.md")
	if err := os.WriteFile(file, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte("tools: {check: Enabled}\ncommandTools: {check: {command: /bin/sh, args: []}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := FreezeCommandPlan(ctx, file, []string{"check"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFrozenCommandPlanRefusesExecutableContentDrift(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.agent.md")
	command := filepath.Join(dir, "command")
	if err := os.WriteFile(file, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf first\n"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := "tools: {check: Enabled}\ncommandTools:\n  check:\n    command: " + command + "\n    args: []\n"
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := FreezeCommandPlan(t.Context(), file, []string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := OpenCommandPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	call := api.ToolCall{ID: "call", Tool: "check", Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: "one"}
	if _, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, call); err == nil {
		t.Fatal("changed executable content retained frozen command authority")
	}
}
