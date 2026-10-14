package app

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestLiftPickerReplacesStaticHeaderBeforeFirstFrame(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, slave := chatPTY(t, 80)
	b := &orkaChatBackend{app: &App{Out: slave}, liftHeader: &liftHeader{agent: "demo", source: "kind-local"}}
	b.paintLiftHeader()
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "6 Connect") })
	boundary := captured.Len()
	done := make(chan error, 1)
	go func() {
		_, err := runChatPicker(t.Context(), slave, slave, chatPicker{
			title: "LIFT · Azure subscription", header: b.liftHeader,
			items: []chatPickerItem{{name: "subscription-one"}},
		})
		done <- err
	}()
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s[boundary:], "subscription-one") })
	frame := captured.String()[boundary:]
	clear := strings.Index(frame, "\x1b[r\x1b[H\x1b[2J")
	header := strings.Index(frame, "KMX / ")
	if clear < 0 || header < clear || strings.Count(ansi.Strip(frame), "KMX / agent:demo") != 1 {
		t.Errorf("pane did not replace static header before drawing: %q", frame)
	}
	if strings.Count(frame, "6 Connect") != 1 {
		t.Errorf("step bar drawn more than once: %q", frame)
	}
	_, _ = io.WriteString(master, "\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("picker did not exit")
	}
}
