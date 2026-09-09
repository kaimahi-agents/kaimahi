package cliui

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

type fdBuffer struct {
	bytes.Buffer
	fd uintptr
}

func (w *fdBuffer) Fd() uintptr { return w.fd }

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestPlainWritersPreserveEveryLabelByte(t *testing.T) {
	p := newOutput(&bytes.Buffer{}, env(nil), func(int) bool { return true }, func(int) (int, int, error) { return 80, 24, nil })
	for label, render := range map[string]func(string) string{
		"PHASE": p.Phase, "DONE": p.Success, "FAILED": p.Failure, "COMPLETE": p.Success,
	} {
		if got := render(label); got != label {
			t.Errorf("plain label=%q, want %q", got, label)
		}
		if got := render(label); strings.Contains(got, "\x1b[") {
			t.Errorf("plain label contains ANSI: %q", got)
		}
	}
}

func TestNonTerminalFileStaysPlain(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got := newOutput(file, env(map[string]string{"TERM": "xterm-256color"}), func(int) bool { return false }, func(int) (int, int, error) { return 80, 24, nil }).Phase("PHASE"); got != "PHASE" {
		t.Fatalf("regular file was styled: %q", got)
	}
}

func TestTerminalPolicyHonorsDumbTermAndNoColor(t *testing.T) {
	w := &fdBuffer{fd: 9}
	for _, values := range []map[string]string{{"TERM": "dumb"}, {"TERM": "xterm", "NO_COLOR": "anything"}} {
		output := newOutput(w, env(values), func(fd int) bool { return fd == 9 }, func(int) (int, int, error) { return 80, 24, nil })
		if got := output.Failure("FAILED"); got != "FAILED" {
			t.Fatalf("disabled styling produced %q for %v", got, values)
		}
		if values["NO_COLOR"] != "" && !output.Rich() {
			t.Fatal("NO_COLOR disabled hierarchy instead of color only")
		}
	}
}

func TestUsableDestinationEnablesStyling(t *testing.T) {
	w := &fdBuffer{fd: 9}
	p := newOutput(w, env(map[string]string{"TERM": "xterm-256color"}), func(fd int) bool { return fd == 9 }, func(int) (int, int, error) { return 80, 24, nil })
	if got := p.Success("DONE"); !strings.Contains(got, "\x1b[") {
		t.Fatalf("usable terminal was not styled: %q", got)
	}
}

func TestStyledPresenterUsesSemanticLipGlossStyles(t *testing.T) {
	p := WithCapabilities(Capabilities{Rich: true, Color: true, Width: 80})
	for name, got := range map[string]string{
		"phase":   p.Phase("PHASE"),
		"success": p.Success("DONE"),
		"failure": p.Failure("FAILED"),
	} {
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("%s label was not styled: %q", name, got)
		}
	}
}
