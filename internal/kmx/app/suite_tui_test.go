package app

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSuiteConsoleCreatesLocalSourceWithoutCluster(t *testing.T) {
	root := t.TempDir()
	m := newSuiteConsole(&App{Out: io.Discard, Err: io.Discard}, SuiteConsoleOptions{Workspace: root})
	m.ctx = context.Background()
	model, _ := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = model.(suiteConsole)
	for _, value := range []string{"hello", "exact instructions", "8192", "1024"} {
		m.input.SetValue(value)
		model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = model.(suiteConsole)
		if cmd != nil {
			model, _ = m.Update(cmd())
			m = model.(suiteConsole)
		}
	}
	entries, err := ListSuiteWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Source != filepath.Join(root, "hello") {
		t.Fatal(entries)
	}
	if m.busy || m.form != "" {
		t.Fatal("create did not finish")
	}
}

func TestSuiteConsoleCancelReviewDoesNotApply(t *testing.T) {
	m := newSuiteConsole(&App{Out: io.Discard, Err: io.Discard}, SuiteConsoleOptions{Workspace: t.TempDir()})
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil || model.(suiteConsole).plan != nil {
		t.Fatal("cancel submitted an operation")
	}
}
