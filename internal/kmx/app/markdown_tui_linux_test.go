package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownPTYUsesSharedEditorAndReturns(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a := mdFixture(t)
	master, slave := chatPTY(t, 120)
	s := NewMarkdownSession(slave, slave)
	s.Provider = "copilot"
	s.executor = &mdFakeExecutor{answer: "Hello, Sam!"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ChatTUI(ctx, a, slave) }()
	var capture strings.Builder
	chatPTYReadUntil(t, master, &capture, func(v string) bool { return strings.Contains(ansi.Strip(v), "Enter send") })
	_, _ = io.WriteString(master, "hello world\x17Sam\r")
	chatPTYReadUntil(t, master, &capture, func(v string) bool { return strings.Contains(ansi.Strip(v), "Hello, Sam!") })
	_, _ = io.WriteString(master, "/show\r")
	chatPTYReadUntil(t, master, &capture, func(v string) bool { return strings.Contains(ansi.Strip(v), "Greet the user.") })
	_, _ = io.WriteString(master, "/quit\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("TUI did not terminate")
	}
}
