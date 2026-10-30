package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func timelineKey(m chatTimelineModel, code rune, mod tea.KeyMod, text string) chatTimelineModel {
	updated, _ := m.Update(tea.KeyPressMsg{Code: code, Mod: mod, Text: text})
	return updated.(chatTimelineModel)
}

func TestTimelineEditorWordDeleteAndCursorJumps(t *testing.T) {
	m := newChatTimeline("agent", "kind-test", "", false)
	m.resize()
	m.editor.SetValue("hello world")
	m.editor.MoveToEnd()
	m = timelineKey(m, 'w', tea.ModCtrl, "")
	if m.editor.Value() != "hello " {
		t.Fatalf("ctrl+w=%q", m.editor.Value())
	}
	m = timelineKey(m, 'a', tea.ModCtrl, "")
	m = timelineKey(m, 'X', 0, "X")
	if m.editor.Value() != "Xhello " {
		t.Fatalf("ctrl+a insert=%q", m.editor.Value())
	}
	m = timelineKey(m, 'e', tea.ModCtrl, "")
	m = timelineKey(m, '!', 0, "!")
	if m.editor.Value() != "Xhello !" {
		t.Fatalf("ctrl+e insert=%q", m.editor.Value())
	}
}

func TestTimelineInputKeepsAgentBelowMultilineDraft(t *testing.T) {
	m := newChatTimeline("project-tester", "local", "", false)
	for _, size := range [][2]int{{80, 24}, {40, 16}, {8, 10}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(chatTimelineModel)
		m.editor.SetValue("first line\nsecond line")
		view := m.inputView()
		rows := strings.Split(ansi.Strip(view), "\n")
		want := ansi.Truncate(m.agent, max(1, size[0]-4), "…")
		if strings.TrimSpace(rows[len(rows)-1]) != want || strings.Contains(ansi.Strip(view), "YOU >") {
			t.Fatalf("input label misplaced or repeated prompt retained:\n%s", view)
		}
		if lipgloss.Width(view) != size[0] || len(rows) != m.editor.Height()+2 {
			t.Fatalf("input geometry changed: %dx%d", lipgloss.Width(view), len(rows))
		}
		if m.editor.Value() != "first line\nsecond line" {
			t.Fatal("rendering changed draft")
		}
		if size[0] >= 40 && (!strings.Contains(ansi.Strip(view), "first line") || !strings.Contains(ansi.Strip(view), "second line")) {
			t.Fatal("multiline draft missing from input")
		}
	}
}

func TestTimelineRecallPreservesDraftAndSkipsCommands(t *testing.T) {
	m := newChatTimeline("agent", "kind-test", "", false)
	m.send("first")
	m.busy = false
	m.send("second")
	m.busy = false
	m.editor.SetValue("draft")
	m = timelineKey(m, tea.KeyUp, 0, "")
	if m.editor.Value() != "second" {
		t.Fatal("latest not recalled")
	}
	m = timelineKey(m, tea.KeyUp, 0, "")
	if m.editor.Value() != "first" {
		t.Fatal("older message not recalled")
	}
	m = timelineKey(m, tea.KeyDown, 0, "")
	m = timelineKey(m, tea.KeyDown, 0, "")
	if m.editor.Value() != "draft" {
		t.Fatalf("draft lost: %q", m.editor.Value())
	}
	m.editor.SetValue("/help")
	m = timelineKey(m, tea.KeyEnter, 0, "")
	if len(m.sent) != 2 {
		t.Fatal("slash command entered sent-message history")
	}
}

func TestTimelineScrollKeepsHeaderAndEditorPinned(t *testing.T) {
	m := newChatTimeline("agent", "kind-test", "", false)
	m.resize()
	for i := 0; i < 50; i++ {
		m.appendEvent(chatTimelineEvent{kind: "assistant", agent: "agent", text: fmt.Sprintf("reply %02d", i), start: true})
	}
	m.editor.SetValue("unsent draft")
	before := strings.Split(ansi.Strip(m.View().Content), "\n")
	m.busy = true
	m = timelineKey(m, tea.KeyPgUp, 0, "")
	after := strings.Split(ansi.Strip(m.View().Content), "\n")
	if m.history.AtBottom() {
		t.Fatal("PageUp did not scroll during response")
	}
	if strings.Join(before[:4], "\n") != strings.Join(after[:4], "\n") {
		t.Fatal("header moved")
	}
	if strings.Join(before[len(before)-6:len(before)-1], "\n") != strings.Join(after[len(after)-6:len(after)-1], "\n") {
		t.Fatal("message editor moved")
	}
	if m.editor.Value() != "unsent draft" {
		t.Fatal("scroll modified draft")
	}
	m = timelineKey(m, tea.KeyEnd, tea.ModCtrl, "")
	if !m.history.AtBottom() {
		t.Fatal("ctrl+end did not return to latest")
	}
}

func TestTimelineToolCallIndentedAndResizeFits(t *testing.T) {
	m := newChatTimeline("agent", "kind-test", "", false)
	m.appendEvent(chatTimelineEvent{kind: "agent", agent: "agent"})
	m.appendEvent(chatTimelineEvent{kind: "agent-operation", agent: "agent", label: "TOOL CALL", text: "Tool: k8s-get-resources\nExecuting registered HTTP tool"})
	for _, size := range [][2]int{{80, 24}, {40, 16}, {28, 12}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(chatTimelineModel)
		view := m.View().Content
		if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
			t.Fatalf("view %dx%d exceeds %v:\n%s", lipgloss.Width(view), lipgloss.Height(view), size, view)
		}
		content := ansi.Strip(m.history.GetContent())
		if !strings.Contains(content, "    [TOOL CALL]") || !strings.Contains(content, "      Tool:") {
			t.Fatalf("tool call not nested:\n%s", content)
		}
	}
}
