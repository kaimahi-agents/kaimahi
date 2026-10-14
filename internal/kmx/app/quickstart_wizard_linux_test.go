package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestQuickstartWizardPTYCtrlCRestoresTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, detecting := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent choice", true: "detection"}[detecting], func(t *testing.T) {
			master, slave := chatPTY(t, 100)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, cancelled, setupErr, err := runQuickstartWizard(slave, slave,
					CreateOptions{descriptionDefault: "Hello world agent"}, nil, quickstartTarget{}, localModel{}, make(chan *localModel, 1), cancel,
					func(report func(quickstartSetupEvent)) error {
						report(quickstartSetupEvent{step: 0, status: "active"})
						<-ctx.Done()
						return ctx.Err()
					})
				if err == nil && (!cancelled || !errors.Is(setupErr, context.Canceled)) {
					err = errors.New("wizard did not cancel setup")
				}
				done <- err
			}()
			var captured strings.Builder
			chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Start with") })
			if detecting {
				if _, err := io.WriteString(master, "\r"); err != nil {
					t.Fatal(err)
				}
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Detecting models before") })
			}
			if _, err := io.WriteString(master, "\x03"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Ctrl-C did not release the wizard and setup")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("terminal was not restored: %v", err)
			}
		})
	}
}

func TestQuickstartChatFullScreenTransitionSettlesBeforeInput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Stdin: slave, Out: slave, Err: slave}
	backend := &staticChatBackend{}
	done := make(chan error, 1)
	go func() { done <- a.runInteractiveChatBackend(backend) }()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "\x1b[?1049h") })
	// Emulate terminal layout changes caused by entering chat's alternate screen,
	// after the wizard's earlier main-screen stabilization would have completed.
	for _, width := range []uint16{90, 100} {
		time.Sleep(60 * time.Millisecond)
		if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: width}); err != nil {
			t.Fatal(err)
		}
	}
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "YOU >") })
	if _, err := io.WriteString(master, "/exit\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("chat rejected its own screen transition: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("chat did not accept /exit after screen transition")
	}
	if len(backend.messages) != 0 {
		t.Fatalf("unexpected submissions: %v", backend.messages)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatalf("chat did not restore the terminal: %v", err)
	}
}

func TestChatTerminalStabilizationRespectsCancellation(t *testing.T) {
	_, slave := chatPTY(t, 80)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitForStableTerminalSizeContext(ctx, slave, slave) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat startup ignored cancellation")
	}
}

func TestChatPickerClearsBeforeReturningToMessage(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, key := range []string{"\r", "\x1b"} {
		master, slave := chatPTY(t, 60)
		done := make(chan error, 1)
		go func() {
			_, err := runChatPicker(context.Background(), slave, slave, chatPicker{title: "TOOLS", multiple: true, items: []chatPickerItem{{name: "k8s-get-resources", enabled: true}}})
			done <- err
		}()
		var captured strings.Builder
		chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "k8s-get-resources") })
		_, _ = io.WriteString(master, key)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("picker did not close")
		}
		_, _ = io.WriteString(slave, "Tools summary\nYOU > \nEND\n")
		chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
		text := captured.String()
		clear := strings.LastIndex(text, "\x1b[H\x1b[2J")
		if clear < 0 || clear > strings.Index(text, "Tools summary") || strings.Contains(text[clear:], "k8s-get-resources") {
			t.Fatalf("picker not cleared before chat: %q", text)
		}
	}
}
