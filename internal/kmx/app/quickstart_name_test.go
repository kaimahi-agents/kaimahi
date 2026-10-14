package app

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQuickstartNameDebounceDropsOldInputAndResults(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Description: "Demo", Namespace: OrkaNamespace})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	m := quickstartWizardModel{create: create, nameCheck: func(context.Context, string, string) error { calls++; return errors.New("Name already exists") }}
	first := m.scheduleNameCheck()
	if first == nil {
		t.Fatal("no initial debounce")
	}
	old := quickstartNameDebounce{m.nameRevision, m.nameValue, m.nameNamespace}
	m.create.input.SetValue("new-name")
	if m.scheduleNameCheck() == nil {
		t.Fatal("edit did not debounce")
	}
	updated, cmd := m.Update(old)
	m = updated.(quickstartWizardModel)
	if cmd != nil || calls != 0 {
		t.Fatal("stale debounce queried server")
	}
	current := quickstartNameDebounce{m.nameRevision, m.nameValue, m.nameNamespace}
	updated, cmd = m.Update(current)
	m = updated.(quickstartWizardModel)
	if cmd == nil {
		t.Fatal("current debounce did not query")
	}
	result := cmd()
	updated, _ = m.Update(result)
	m = updated.(quickstartWizardModel)
	if calls != 1 || m.nameErr == nil || !m.nameChecked || m.namePending {
		t.Fatal("collision not surfaced")
	}
	m.create.input.SetValue("available")
	m.scheduleNameCheck()
	updated, _ = m.Update(result)
	m = updated.(quickstartWizardModel)
	if m.nameErr != nil || m.nameChecked {
		t.Fatal("stale result overwrote new input")
	}
}

func TestQuickstartNameEnterWaitsForCheck(t *testing.T) {
	create, err := newQuickstartSourceForm(CreateOptions{Description: "Demo", Namespace: OrkaNamespace, ProviderType: "openai", Model: "test", Secret: "key"})
	if err != nil {
		t.Fatal(err)
	}
	m := quickstartWizardModel{create: create, nameCheck: func(context.Context, string, string) error { return nil }}
	m.scheduleNameCheck()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(quickstartWizardModel)
	if m.create.step != createName {
		t.Fatal("name submitted before check")
	}
	updated, _ = m.Update(quickstartNameResult{quickstartNameDebounce{m.nameRevision, m.nameValue, m.nameNamespace}, nil})
	m = updated.(quickstartWizardModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated.(quickstartWizardModel).create.step != quickstartInstructionsPurpose {
		t.Fatal("available name did not advance")
	}
}
