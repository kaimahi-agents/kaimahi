// Package kubectlfake shares the fake kubectl protocol between lifecycle tests
// and their executable helper. Installed commands do not import this package.
package kubectlfake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Request carries process inputs without changing process-global state.
type Request struct {
	Context        context.Context
	Args           []string
	Getenv         func(string) string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// ExitError represents a nonzero fixture exit, not an application diagnostic.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("kubectl fixture exited with status %d", e.Code)
}

type exitStatus int

func exit(code int) { panic(exitStatus(code)) }

// recordingWriter observes errors even when legacy fmt/json callers ignore them.
type recordingWriter struct {
	writer io.Writer
	err    error
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if w.err == nil {
		w.err = err
	}
	return n, err
}

func dispatch(r Request, handler func(Request)) (err error) {
	stdout, stderr := &recordingWriter{writer: r.Stdout}, &recordingWriter{writer: r.Stderr}
	r.Stdout, r.Stderr = stdout, stderr
	defer func() {
		if value := recover(); value != nil {
			status, ok := value.(exitStatus)
			if !ok {
				panic(value)
			}
			if status != 0 {
				err = &ExitError{Code: int(status)}
			}
		}
		// A successful exit must not hide a failed output write or cancellation.
		err = errors.Join(err, stdout.err, stderr.err, r.Context.Err())
	}()
	if err := r.Context.Err(); err != nil {
		return err
	}
	handler(r)
	return nil
}

func Reconcile(r Request) error  { return dispatch(r, func(r Request) { r.reconcile() }) }
func Kagent(r Request) error     { return dispatch(r, func(r Request) { r.kagent() }) }
func Pins(r Request) error       { return dispatch(r, func(r Request) { r.pins() }) }
func KagentPins(r Request) error { return dispatch(r, func(r Request) { r.kagentPins() }) }

func (r Request) delay(duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-r.Context.Done():
		exit(1)
	case <-timer.C:
	}
}

// Environment follows exec.Cmd.Environ semantics, including last duplicate wins.
func Environment(values []string) func(string) string {
	env := make(map[string]string, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if ok {
			env[key] = val
		}
	}
	return func(key string) string { return env[key] }
}
