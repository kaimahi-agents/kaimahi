package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestChatRejectsInteractiveJSONBeforeLoadingApplication(t *testing.T) {
	for _, flags := range [][]string{{"--interactive", "--json"}, {"--json", "--interactive=true"}} {
		deps := dependencies{stdout: io.Discard, stderr: io.Discard, loadConfig: func(string, string) (*config.Config, error) {
			t.Fatal("conflicting chat flags reached configuration or operations")
			return nil, nil
		}}
		err := execute(append([]string{"agent", "chat", "agent"}, flags...), deps)
		if err == nil || !strings.Contains(err.Error(), "--interactive and --json") {
			t.Fatalf("expected flag conflict, got %v", err)
		}
	}
}

// TestChatTreatsExplicitFalseInteractiveLikeOmittedInteractive locks in that
// "--interactive=false --json" is not a conflict: an explicit false resolves
// to the same value as an omitted flag, and the guard compares resolved
// values, not whether the flag was set.
func TestChatTreatsExplicitFalseInteractiveLikeOmittedInteractive(t *testing.T) {
	sentinel := errors.New("reached configuration past flag validation")
	for _, flags := range [][]string{{"--json"}, {"--interactive=false", "--json"}} {
		loads := 0
		deps := dependencies{stdout: io.Discard, stderr: io.Discard, loadConfig: func(string, string) (*config.Config, error) {
			loads++
			return nil, sentinel
		}}
		err := execute(append([]string{"agent", "chat", "agent"}, flags...), deps)
		if !errors.Is(err, sentinel) {
			t.Fatalf("%v: expected flag validation to pass and reach loadConfig, got %v", flags, err)
		}
		if loads != 1 {
			t.Fatalf("%v: expected loadConfig once, got %d", flags, loads)
		}
	}
}
