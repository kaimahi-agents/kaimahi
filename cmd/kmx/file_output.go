package main

import (
	"errors"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

type fileOutput struct {
	observer                                 *app.MachineFileObserver
	result                                   app.FilePreviewResult
	entered, preflightStarted, preflightDone bool
}

func newFileOutput(out io.Writer) *machineOutput {
	m := newMachineOutput(out, "file.test")
	m.file = &fileOutput{}
	m.file.observer = app.NewMachineFileObserver("", func(p app.MachineFileProgress) error { return m.fileProgress(p) })
	return m
}
func (m *machineOutput) fileProgress(p app.MachineFileProgress) error {
	if p.Stage == "preflight" && p.Event == "stage.finished" {
		m.file.preflightDone = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeLocked(struct {
		app.MachineEnvelope
		app.MachineFileProgress
	}{m.envelope("kmx.event"), p}, false)
}
func (m *machineOutput) fileSummary(c app.MachineFileCase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeLocked(struct {
		app.MachineEnvelope
		Event string `json:"event"`
		app.MachineFileCase
	}{m.envelope("kmx.event"), "case.summary", c}, false)
}
func (m *machineOutput) finishFile(cause error) error {
	if !m.file.entered && cause == nil {
		return nil
	} // Successful help/completion stays inert.
	r := m.file.result
	p := m.file.observer.Final(r) // Freeze before checking writer state.
	outputFailure := func(err error) error {
		return &machineCommandError{cause: err, safe: app.NewMachineError("output_failed", "")}
	}
	if m.err != nil {
		return outputFailure(m.err)
	}
	if !m.file.preflightStarted {
		m.file.preflightStarted = true
		if err := m.event("preflight", "active"); err != nil {
			return outputFailure(err)
		}
	}
	if !m.file.preflightDone {
		if err := m.event("preflight", "failed"); err != nil {
			return outputFailure(err)
		}
	}
	outcome, verdict, code := "failed", "unknown", "operation_failed"
	exit := 0
	if cause != nil {
		exit = exitCode(cause)
	}
	if !m.file.entered {
		code = "invalid_arguments"
	}
	if r.State == "complete" {
		outcome, verdict = "complete", string(r.Verdict)
	} else if r.State == "cancelled" {
		outcome, code = "cancelled", "cancelled"
	} else if r.State == "error" {
		outcome, code = "unknown", "execution_unknown"
	}
	if errors.Is(cause, errWindowsOperationUnsupported) {
		code = "unsupported"
	}
	if r.ReasonCode == "deadline_exceeded" {
		code = "deadline_exceeded"
	}
	if r.ReasonCode == "diagnostics_failed" {
		code = "output_failed"
	}
	if r.State != "complete" {
		verdict = "unknown"
	}
	var safe *app.MachineError
	if cause != nil && outcome != "complete" {
		safe = app.NewMachineError(code, "finalize")
	}
	if err := m.event("finalize", "active"); err != nil {
		return outputFailure(err)
	}
	status := "done"
	if outcome != "complete" {
		status = "failed"
	}
	if err := m.event("finalize", status); err != nil {
		return outputFailure(err)
	}
	for _, row := range p.Cases {
		if err := m.fileSummary(row); err != nil {
			return outputFailure(err)
		}
	}
	m.mu.Lock()
	err := m.writeLocked(struct {
		app.MachineResult
		app.MachineFileProjection
	}{app.MachineResult{MachineEnvelope: m.envelope("kmx.result"), Outcome: outcome, Verdict: verdict, ExitCode: exit, Error: safe}, p}, true)
	m.mu.Unlock()
	if err != nil {
		return outputFailure(err)
	}
	if cause != nil {
		if safe == nil {
			safe = app.NewMachineError("operation_failed", "")
		}
		return &machineCommandError{cause: cause, safe: safe}
	}
	return nil
}

// Format recognition is syntactic and grants no execution authority. No source
// read, extension guessing or folder discovery occurs before Cobra/intake.
func fileJSONInvocation(argv []string) bool {
	command, file, afterDash := false, false, false
	output, count := "", 0
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, inline := strings.Cut(arg, "=")
		if afterDash {
			if !file {
				file = true
			}
			continue
		}
		if arg == "--" {
			afterDash = true
			continue
		}
		switch name {
		case "--context", "-context", "--container-engine", "--runner", "--host", "--case", "--tests", "--model", "--provider", "--provider-url", "--tool-bindings", "--mcp-server":
			if !inline {
				i++
			}
			continue
		case "--help", "-h", "--verbose", "--no-content", "--no-mcp-servers", "--github-summary":
			continue
		}
		if !command {
			if arg != "test" {
				return false
			}
			command = true
			continue
		}
		switch {
		case name == "--output" || arg == "-o":
			if !inline {
				i++
				if i >= len(argv) {
					return false
				}
				value = argv[i]
			}
			output, count = value, count+1
		case strings.HasPrefix(arg, "-o") && len(arg) > 2:
			output, count = arg[2:], count+1
		case !strings.HasPrefix(arg, "-"):
			if !file && arg == "explain" {
				return false
			}
			file = true
		}
	}
	return command && file && count == 1 && output == "jsonl"
}
