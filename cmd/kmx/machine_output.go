package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

const machineRecordLimit = 64 * 1024

var errMachineTerminal = errors.New("machine output already completed")
var errMachineRecordSize = errors.New("machine output exceeds the record limit")

// machineOutput owns one invocation's terminal write. Both version documents
// and error results pass through the same latch: a partial write is never retried.
type machineOutput struct {
	mu        sync.Mutex
	out       io.Writer
	operation string
	id        string
	started   time.Time
	done      bool
	err       error
	seq       uint64
	cancel    context.CancelFunc
	ctx       context.Context
	stop      func()
	setup     *provisioningOutput
	file      *fileOutput
}

func newMachineOutput(out io.Writer, operation string) *machineOutput {
	return &machineOutput{out: out, operation: operation, id: uuid.NewString(), started: time.Now()}
}

func (m *machineOutput) document(record any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeLocked(record, true)
}

func (m *machineOutput) writeLocked(record any, terminal bool) error {
	if m.err != nil {
		return m.err
	}
	if m.done {
		return errMachineTerminal
	}
	b, err := json.Marshal(record)
	if err == nil {
		b = append(b, '\n')
		if len(b) > machineRecordLimit {
			err = errMachineRecordSize
		}
	}
	if err == nil {
		var n int
		n, err = m.out.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
	}
	m.err = err
	m.done = err == nil && terminal
	if err != nil && m.cancel != nil {
		m.cancel()
	}
	return err
}

func (m *machineOutput) result(outcome, verdict string, exit int, safe *app.MachineError) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeLocked(app.MachineResult{
		MachineEnvelope: m.envelope("kmx.result"),
		Outcome:         outcome, Verdict: verdict, ExitCode: exit, Error: safe,
	}, true)
}

// The cause remains available to exitCode/errors.Is, but main prints only the
// safe message. Never format the cause into either machine output stream.
type machineCommandError struct {
	cause error
	safe  *app.MachineError
	code  string
}

func (e *machineCommandError) Error() string { return e.safe.Error() }
func (e *machineCommandError) Unwrap() error { return e.cause }

// Recognize the opt-in before preprocessing can fail. Known flag operands
// cannot discover a command. After version is identified, unknown flags only
// affect Cobra's error; keep looking for the requested error format.
func versionJSONInvocation(argv []string) bool {
	command := false
	output, count := "", 0
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, inline := strings.Cut(arg, "=")
		switch name {
		case "--":
			return command && count == 1 && output == "json"
		case "--help", "-h":
			// Cobra decides whether help is enabled, disabled or malformed.
			// Successful help returns nil without writing a machine record.
			continue
		case "--context", "-context", "--container-engine":
			if !inline {
				i++
			}
			continue
		}
		if !command {
			if arg != "version" {
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
		}
	}
	return command && count == 1 && output == "json"
}
