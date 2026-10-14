package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"golang.org/x/sys/unix"
)

func TestFullscreenChatResizePreservesTypedInput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	r.stickyHeader = true
	r.enterFullScreen()
	defer r.leaveFullScreen()
	r.statusStart("demo", "kind-test")
	r.statusEnd()
	r.prompt()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() { line, err := readSlashLine(ctx, slave, slave, r, true); done <- result{line, err} }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "MESSAGE") })
	_, _ = io.WriteString(master, "hello")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "hello") })
	boundary := captured.Len()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 16, Col: 40}); err != nil {
		t.Fatal(err)
	}
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s[boundary:], "MESSAGE") })
	select {
	case result := <-done:
		t.Fatalf("resize ended input: %+v", result)
	default:
	}
	_, _ = io.WriteString(master, " world\r")
	select {
	case result := <-done:
		if result.err != nil || result.line != "hello world" {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input did not submit after resize")
	}
}

func TestOrkaSlashPopupSelectsWithoutSubmittingPrematurely(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	r.slashCommands = orkaSlashCommands
	r.prompt()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() { line, err := readSlashLine(ctx, slave, slave, r, true); done <- result{line, err} }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "MESSAGE") })
	_, _ = io.WriteString(master, "/inf")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "/inference-local") })
	_, _ = io.WriteString(master, "\x1b[B\x1b[B\t")
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(ansi.Strip(s), "YOU > /inference-local") })
	select {
	case <-done:
		t.Fatal("completion submitted without enter")
	default:
	}
	_, _ = io.WriteString(master, "\r")
	select {
	case result := <-done:
		if result.err != nil || result.line != "/inference-local" {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not submit")
	}
}
