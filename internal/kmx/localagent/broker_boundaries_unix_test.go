//go:build !windows

package localagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/residenttools"
)

func brokerBoundaryPlan(t *testing.T, dir, script, probe string) residenttools.Plan {
	t.Helper()
	file := filepath.Join(dir, "helper.agent.md")
	if err := os.WriteFile(file, []byte("# Helper\n"), 0600); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(script)
	config := "commandTools:\n  check:\n    command: /bin/sh\n    args: [-c, " + string(quoted) + "]\n"
	if probe != "" {
		quoted, _ = json.Marshal(probe)
		config += "    checks:\n      readiness:\n        args: [-c, " + string(quoted) + "]\n        timeout: 15s\n        expect: {exitCode: 0, stdoutRegex: '^ready$'}\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	commands, err := localtools.FreezeCommandPlan(t.Context(), file, []string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := residenttools.NewBoundedProcessPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestBrokerLongDeclaredProbeBecomesReady(t *testing.T) {
	plan := brokerBoundaryPlan(t, t.TempDir(), "printf ok", "/bin/sleep 11; printf ready")
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	file, err := residenttools.Start(ctx, "", plan)
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := residenttools.Attach(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerStartupCancellationReapsProbe(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	plan := brokerBoundaryPlan(t, dir, "printf ok", "printf '%s' $$ > "+shellQuote(pidFile)+"; /bin/sleep 30; printf ready")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		file, err := residenttools.Start(ctx, "", plan)
		if file != nil {
			file.Close()
		}
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	pid := 0
	for pid == 0 {
		select {
		case <-deadline.C:
			t.Fatal("probe did not start")
		case err := <-done:
			t.Fatalf("startup ended before cancellation: %v", err)
		case <-tick.C:
			raw, _ := os.ReadFile(pidFile)
			pid, _ = strconv.Atoi(string(raw))
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("startup cancellation did not finish")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("probe leader remains after cancellation: %v", err)
	}
}

func TestBrokerDoesNotExposeDescriptorsAndDeduplicatesWithinSession(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	fds := "for f in 3 4 5; do if test -e /dev/fd/$f; then exit 7; fi; done; "
	plan := brokerBoundaryPlan(t, dir, fds+"printf x >> "+shellQuote(counter)+"; printf ok", fds+"printf ready")
	plan.Binding.Limits.MaxCallsPerTurn = 1
	plan.Binding.Limits.MaxCallsPerSession = 2
	plan.Binding.Limits.MaxCallsPerInstance = 3
	file, err := residenttools.Start(t.Context(), "", plan)
	if err != nil {
		t.Fatal(err)
	}
	client, host, err := residenttools.Attach(file)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func(session, turn, id string, want bool) {
		t.Helper()
		invocation := strings.Repeat(turn, 24)
		result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: session}, api.ToolCall{ID: id, Tool: "check", Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: invocation + ":" + id})
		if want && (err != nil || result.Output["succeeded"] != true) || !want && err == nil {
			t.Fatalf("session=%s turn=%s id=%s: %+v %v", session, turn, id, result, err)
		}
	}
	call("s1", "a", "one", true)
	call("s1", "a", "one", false) // A replay cannot repeat the effect.
	call("s1", "a", "two", false) // Per-turn ceiling.
	call("s1", "b", "one", true)
	call("s1", "c", "one", false) // Per-session ceiling.
	call("s2", "a", "one", true)  // Keys are scoped by Session, as in Sessions.
	call("s2", "b", "one", false) // Per-instance ceiling.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(counter)
	if err != nil || string(raw) != "xxx" {
		t.Fatalf("effects=%q err=%v", raw, err)
	}
}
