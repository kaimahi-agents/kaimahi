package main

import (
	"encoding/json"
	"errors"
	"io"
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
}

func newMachineOutput(out io.Writer, operation string) *machineOutput {
	return &machineOutput{out: out, operation: operation, id: uuid.NewString(), started: time.Now()}
}

func (m *machineOutput) document(record any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.done = err == nil
	return err
}

func (m *machineOutput) result(outcome, verdict string, exit int, safe *app.MachineError) error {
	return m.document(app.MachineResult{
		MachineEnvelope: app.MachineEnvelope{
			SchemaVersion: 1, Kind: "kmx.result", Operation: m.operation,
			OperationID: m.id, Seq: 1, ElapsedMs: time.Since(m.started).Milliseconds(),
		},
		Outcome: outcome, Verdict: verdict, ExitCode: exit, Error: safe,
	})
}

// The cause remains available to exitCode/errors.Is, but main prints only the
// safe message. Never format the cause into either machine output stream.
type machineCommandError struct {
	cause error
	safe  *app.MachineError
}

func (e *machineCommandError) Error() string { return e.safe.Error() }
func (e *machineCommandError) Unwrap() error { return e.cause }
