//go:build windows

package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync/atomic"
)

// Command is an exec.Cmd-compatible Windows command with process-tree
// ownership. Its embedded fields and pipe methods retain exec.Cmd's preparation
// API, while Start, Run, Wait, Output, CombinedOutput and Close preserve the
// ownership boundary. Command.Cmd exists only for integration with APIs typed
// as *exec.Cmd; callers must not invoke its lifecycle methods directly.
type Command struct {
	*exec.Cmd

	ctx         context.Context
	startCalled atomic.Bool
	waitCalled  atomic.Bool
	platform    platformCommand
}

func newCommand(ctx context.Context, name string, args ...string) *Command {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	c := &Command{Cmd: cmd, ctx: ctx}
	c.initPlatform()
	return c
}

// Start starts the command under the platform ownership boundary.
func (c *Command) Start() error {
	if c.startCalled.Swap(true) {
		return errors.New("exec: already started")
	}
	return c.startPlatform()
}

// Wait waits for the command and releases its platform ownership resources.
func (c *Command) Wait() error {
	if !c.startCalled.Load() {
		return errors.New("exec: not started")
	}
	if c.waitCalled.Swap(true) {
		return errors.New("exec: Wait was already called")
	}
	return c.waitPlatform(c.Cmd.Wait())
}

// Run starts the command and waits for it to complete.
func (c *Command) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// String, Environ and the pipe methods expose the embedded command without
// requiring callers to bypass the owned lifecycle through Command.Cmd.
func (c *Command) String() string { return c.Cmd.String() }

func (c *Command) Environ() []string { return c.Cmd.Environ() }

func (c *Command) StdinPipe() (io.WriteCloser, error) { return c.Cmd.StdinPipe() }

func (c *Command) StdoutPipe() (io.ReadCloser, error) { return c.Cmd.StdoutPipe() }

func (c *Command) StderrPipe() (io.ReadCloser, error) { return c.Cmd.StderrPipe() }

// Close terminates the owned process tree. A caller that successfully called
// Start must still call Wait to release os/exec's process resources. Close does
// not release the Job Object; Wait retains it until every descendant is reaped.
// Close may run concurrently with the one allowed Wait call.
func (c *Command) Close() error {
	return c.closePlatform()
}

// Output runs the command and returns its standard output.
func (c *Command) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout bytes.Buffer
	c.Stdout = &stdout

	var stderr *prefixSuffixSaver
	if c.Stderr == nil {
		stderr = &prefixSuffixSaver{N: 32 << 10}
		c.Stderr = stderr
	}
	err := c.Run()
	if err != nil && stderr != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
		}
	}
	return stdout.Bytes(), err
}

// CombinedOutput runs the command and returns its combined standard output and
// standard error.
func (c *Command) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if c.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var output bytes.Buffer
	c.Stdout, c.Stderr = &output, &output
	err := c.Run()
	return output.Bytes(), err
}

// prefixSuffixSaver keeps Output's error capture bounded like os/exec.Output.
type prefixSuffixSaver struct {
	N         int
	prefix    []byte
	suffix    []byte
	suffixOff int
	skipped   int64
}

func (w *prefixSuffixSaver) Write(p []byte) (int, error) {
	written := len(p)
	p = w.fill(&w.prefix, p)
	if overage := len(p) - w.N; overage > 0 {
		p = p[overage:]
		w.skipped += int64(overage)
	}
	p = w.fill(&w.suffix, p)
	for len(p) > 0 {
		n := copy(w.suffix[w.suffixOff:], p)
		p = p[n:]
		w.skipped += int64(n)
		w.suffixOff = (w.suffixOff + n) % w.N
	}
	return written, nil
}

func (w *prefixSuffixSaver) fill(dst *[]byte, p []byte) []byte {
	if remaining := w.N - len(*dst); remaining > 0 {
		n := min(len(p), remaining)
		*dst = append(*dst, p[:n]...)
		p = p[n:]
	}
	return p
}

func (w *prefixSuffixSaver) Bytes() []byte {
	if w.suffix == nil {
		return append([]byte(nil), w.prefix...)
	}
	if w.skipped == 0 {
		return append(append([]byte(nil), w.prefix...), w.suffix...)
	}
	var output bytes.Buffer
	output.Write(w.prefix)
	output.WriteString("\n... omitting ")
	output.WriteString(strconv.FormatInt(w.skipped, 10))
	output.WriteString(" bytes ...\n")
	output.Write(w.suffix[w.suffixOff:])
	output.Write(w.suffix[:w.suffixOff])
	return output.Bytes()
}
