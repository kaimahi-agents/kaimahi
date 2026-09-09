package main

import (
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestChatRejectsInteractiveJSONBeforeLoadingApplication(t *testing.T) {
	for _, flags := range [][]string{{"--interactive", "--json"}, {"--json", "--interactive=true"}} {
		deps := dependencies{stdout: io.Discard, stderr: io.Discard, loadConfig: func(string) (*config.Config, error) {
			t.Fatal("conflicting chat flags reached configuration or operations")
			return nil, nil
		}}
		err := execute(append([]string{"agent", "chat", "agent"}, flags...), deps)
		if err == nil || !strings.Contains(err.Error(), "--interactive and --json") {
			t.Fatalf("expected flag conflict, got %v", err)
		}
	}
}
