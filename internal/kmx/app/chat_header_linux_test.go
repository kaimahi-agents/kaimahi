package app

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStickyChatHeaderSetsAndRestoresScrollRegion(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 60)
	r := newChatRenderer(slave)
	r.stickyHeader = true
	r.enterFullScreen()
	r.statusStart("demo", "kind-test")
	r.statusSection("Location", "local-kind-test")
	r.statusSection("Inference", "Copilot · auto")
	r.statusSection("Tools", "(1 tool enabled) k8s-get-resources")
	r.statusEnd()
	r.assistant("demo", "answer", true)
	r.finish()
	r.statusStart("remote-agent", "aks-prod")
	r.statusSection("Location", "remote-aks-prod")
	r.statusEnd()
	r.leaveFullScreen()
	_, _ = io.WriteString(slave, "END\n")
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
	text := captured.String()
	for _, want := range []string{"agent:demo", "location:local-kind-test", "agent:remote-agent", "location:remote-aks-prod", "Copilot", "k8s-get-resources", "\x1b[5;24r", "\x1b[r\x1b[?1049l"} {
		if !strings.Contains(text, want) && !strings.Contains(ansi.Strip(text), want) {
			t.Fatalf("missing %q: %q", want, text)
		}
	}
	if r.headerRows != 0 {
		t.Fatal("scroll region retained")
	}
}

func TestToolsRefreshReanchorsPromptBelowHeader(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave := chatPTY(t, 80)
	r := newChatRenderer(slave)
	r.stickyHeader = true
	r.enterFullScreen()
	defer r.leaveFullScreen()
	r.statusStart("demo", "kind-test")
	r.statusEnd()
	r.suspendStickyHeader()
	// /tools save refreshes the count before control returns to the chat loop.
	r.statusSection("Tools", "(1 tool enabled) k8s-get-resources")
	r.suspendStickyHeader()
	_, _ = io.WriteString(slave, "\x1b[H\x1b[2J")
	r.statusStart("demo", "kind-test")
	r.statusSection("Tools", "(1 tool enabled) k8s-get-resources")
	r.statusEnd()
	_, _ = io.WriteString(slave, "PROMPT\nEND\n")
	var captured strings.Builder
	chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.HasSuffix(s, "END\r\n") })
	if !strings.Contains(captured.String(), "\x1b[5;24r\x1b[5;1HPROMPT") {
		t.Fatalf("prompt not anchored below header: %q", captured.String())
	}
}
