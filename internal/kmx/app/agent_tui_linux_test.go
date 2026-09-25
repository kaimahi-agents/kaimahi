package app

import (
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAgentTUIDemoPTYCommandsAndTerminalRestore(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave := chatPTY(t, 100)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Stdin: slave, Out: slave, Err: slave}
	done := make(chan error, 1)
	go func() { done <- a.AgentTUI(AgentTUIOptions{Demo: true}) }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "AGENTS") })
	_, _ = io.WriteString(master, "n")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "CREATE AGENT") })
	_, _ = io.WriteString(master, "\x1b")
	time.Sleep(100 * time.Millisecond)
	_, _ = io.WriteString(master, "/lift ")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "suggestions") })
	_, _ = io.WriteString(master, "\x03")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dashboard did not exit")
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatalf("terminal not restored: %v", err)
	}
}
