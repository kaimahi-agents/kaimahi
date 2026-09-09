package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

type styledProgress struct{}

func (styledProgress) Phase(text string) string   { return "\x1b[36m" + text + "\x1b[0m" }
func (styledProgress) Success(text string) string { return "\x1b[32m" + text + "\x1b[0m" }
func (styledProgress) Failure(text string) string { return "\x1b[31m" + text + "\x1b[0m" }
func (styledProgress) Heading(text string) string { return "\x1b[36m" + text + "\x1b[0m" }
func (styledProgress) Warning(text string) string { return "\x1b[33m" + text + "\x1b[0m" }
func (styledProgress) Accent(text string) string  { return "\x1b[35m" + text + "\x1b[0m" }
func (styledProgress) Muted(text string) string   { return "\x1b[90m" + text + "\x1b[0m" }
func (styledProgress) Info(text string) string    { return "\x1b[34m" + text + "\x1b[0m" }

func TestRunPhaseDelimitsNativeOutputAndReportsElapsedTime(t *testing.T) {
	var out bytes.Buffer
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(0, 0).Add(1250 * time.Millisecond),
	}
	a := &App{Err: &out, now: func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}}

	err := a.runPhase(phase{current: 2, total: 6, name: "Deploy Ollama"}, func() error {
		out.WriteString("kubectl apply output\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "\nPHASE  [2/6] Deploy Ollama\n" +
		"kubectl apply output\n" +
		"DONE   [2/6] Deploy Ollama (1.3s)\n"
	if out.String() != want {
		t.Fatalf("unexpected phase transcript:\n%q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("redirected phase transcript contains ANSI: %q", out.String())
	}
}

func TestStyledProgressPreservesOutputOrderAndStdout(t *testing.T) {
	var out, errOut bytes.Buffer
	times := []time.Time{time.Unix(0, 0), time.Unix(2, 0)}
	a := &App{Out: &out, Err: &errOut, progressUI: styledProgress{}, now: func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}}
	if err := a.runPhase(phase{current: 2, total: 6, name: "Deploy Ollama"}, func() error {
		errOut.WriteString("native output\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	text := errOut.String()
	if !strings.Contains(text, "PHASE") || !strings.Contains(text, "DONE") || !strings.Contains(text, "\x1b[") {
		t.Fatalf("styled transcript lacks semantic labels or ANSI: %q", text)
	}
	if phase, native, done := strings.Index(text, "PHASE"), strings.Index(text, "native output"), strings.Index(text, "DONE"); !(phase < native && native < done) {
		t.Fatalf("native output moved outside phase boundaries: %q", text)
	}
	if out.Len() != 0 {
		t.Fatalf("progress contaminated stdout: %q", out.String())
	}
}

func TestRunPhaseReportsFailureWithoutHidingTheError(t *testing.T) {
	var out bytes.Buffer
	times := []time.Time{time.Unix(0, 0), time.Unix(2, 0)}
	a := &App{Err: &out, now: func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}}
	wantErr := errors.New("rollout timed out")
	err := a.runPhase(phase{current: 3, total: 3, name: "Deploy and verify plane"}, func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("phase replaced its underlying error: %v", err)
	}
	if !strings.Contains(out.String(), "FAILED [3/3] Deploy and verify plane (2s)") {
		t.Fatalf("failure boundary missing from %q", out.String())
	}
}

func TestEnhancedProgressKeepsNativeOutputVisible(t *testing.T) {
	var out bytes.Buffer
	times := []time.Time{time.Unix(0, 0), time.Unix(2, 0)}
	a := &App{Err: &out, progressUI: styledProgress{}, now: func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}}
	if err := a.runPhase(phase{current: 4, total: 6, name: "Install or verify kagent"}, func() error {
		out.WriteString("helm output\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[") || !strings.Contains(out.String(), "PHASE") || !strings.Contains(out.String(), "DONE") || !strings.Contains(out.String(), "helm output") {
		t.Fatalf("unexpected enhanced transcript:\n%q", out.String())
	}
}

func TestEnhancedProgressUsesColorOnlyWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	times := []time.Time{time.Unix(0, 0), time.Unix(0, 0)}
	a := &App{Err: &out, progressUI: styledProgress{}, now: func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}}
	if err := a.runPhase(phase{current: 1, total: 1, name: "Test"}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\033[36m") || !strings.Contains(out.String(), "\033[32m") {
		t.Fatalf("colored progress lacks ANSI styling: %q", out.String())
	}
}

func TestFormatElapsedUsesUsefulPrecision(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{345 * time.Millisecond, "350ms"},
		{12*time.Second + 349*time.Millisecond, "12.3s"},
		{2*time.Minute + 29*time.Second + 600*time.Millisecond, "2m30s"},
	} {
		if got := formatElapsed(tc.elapsed); got != tc.want {
			t.Errorf("formatElapsed(%s)=%q, want %q", tc.elapsed, got, tc.want)
		}
	}
}
