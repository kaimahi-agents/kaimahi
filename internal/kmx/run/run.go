// Package run executes external cluster, container and cloud tools for kmx.
// It manages command arguments, environment overrides, output and cancellation.
package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes external commands. Every field has a working zero value
// except Stdout/Stderr, which Default fills in.
type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
	// Env is added to the child's environment (KIND_EXPERIMENTAL_PROVIDER
	// for the podman path).
	Env []string
	// Unset names inherited variables to remove from child process environments.
	// Env additions take precedence when the same name appears in both lists.
	Unset []string
	// Echo prints each command before running it, like make does.
	Echo bool
	// Context bounds every child command when set. Interactive coordinators use
	// it to stop active downloads and waits when their UI is cancelled.
	Context context.Context
}

// Default returns a Runner wired to the process's own streams.
func Default() *Runner {
	return &Runner{Stdout: os.Stdout, Stderr: os.Stderr, Echo: true}
}

func (r *Runner) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	if r.Context != nil {
		c = exec.CommandContext(r.Context, name, args...)
	}
	if len(r.Env) > 0 || len(r.Unset) > 0 {
		c.Env = environ(os.Environ(), r.Env, r.Unset)
	}
	return c
}

// environ applies additions and removals to a base environment. Removals are
// applied to the base only: a variable both set and unset is set, since the
// caller naming a value said so more specifically.
func environ(base, add, unset []string) []string {
	out := make([]string, 0, len(base)+len(add))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, u := range unset {
			if name == u {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return append(out, add...)
}

func (r *Runner) echo(name string, args []string) {
	if !r.Echo {
		return
	}
	parts := append([]string{name}, args...)
	fmt.Fprintln(r.Stderr, strings.Join(parts, " "))
}

// Run streams the command's output and fails on a non-zero exit.
func (r *Runner) Run(name string, args ...string) error {
	r.echo(name, args)
	c := r.cmd(name, args...)
	c.Stdout, c.Stderr = r.Stdout, r.Stderr
	if err := c.Run(); err != nil {
		if r.Context != nil && r.Context.Err() != nil {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), r.Context.Err())
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// RunStdin streams the command's output and feeds it the given bytes — this
// is how the embedded manifests are applied, since there is no file on disk
// to point `kubectl apply -f` at.
func (r *Runner) RunStdin(stdin []byte, name string, args ...string) error {
	r.echo(name, args)
	c := r.cmd(name, args...)
	c.Stdin = bytes.NewReader(stdin)
	c.Stdout, c.Stderr = r.Stdout, r.Stderr
	if err := c.Run(); err != nil {
		if r.Context != nil && r.Context.Err() != nil {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), r.Context.Err())
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Capture returns the command's stdout, trimmed, plus stderr on failure.
// Nothing is echoed: these are reads, and a status command that printed
// every query it made would be unreadable.
func (r *Runner) Capture(name string, args ...string) (string, error) {
	c := r.cmd(name, args...)
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	if err != nil {
		return strings.TrimSpace(out.String()),
			fmt.Errorf("%s: %s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Quiet reports whether the command succeeded, discarding all output. It is
// the `>/dev/null 2>&1` of the Makefile's existence checks.
func (r *Runner) Quiet(name string, args ...string) bool {
	c := r.cmd(name, args...)
	return c.Run() == nil
}

// Poll calls check up to attempts times, sleeping interval between tries. It
// is the loop behind every `for _ in $(seq 1 N)` in the Makefile: a bounded
// wait that fails loudly rather than a sleep that guesses.
//
// Bounded by ATTEMPTS, not by wall-clock, because that is what the shell did
// and the difference is not cosmetic. Each check here is a real API call, and
// on a slow path — kind on podman inside a VM, say — those calls are what
// gets slower. A wall-clock budget silently converts "120 tries" into
// "however few tries fit in 120 seconds", so the wait gets *shorter* exactly
// on the machines that need it to be longer, and a rollout race the wait
// exists to absorb comes back as "the agent is not answering".
func Poll(attempts int, interval time.Duration, check func() bool) bool {
	return PollContext(context.Background(), attempts, interval, check)
}

// PollContext also stops between checks and during retry delays on cancellation.
func PollContext(ctx context.Context, attempts int, interval time.Duration, check func() bool) bool {
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return false
		}
		if check() {
			return true
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(interval):
			}
		}
	}
	return false
}

// MustExist fails with an actionable message when a required tool is not on
// PATH, rather than letting exec report "executable file not found".
func MustExist(tool, why, install string) error {
	if _, err := exec.LookPath(tool); err != nil {
		return fmt.Errorf("%s is not on PATH — kmx needs it %s.\n  install: %s", tool, why, install)
	}
	return nil
}

// Command returns a prepared command for callers that need to manage the
// process themselves — the chat port-forward, which runs in the background
// and has to be waited on and killed.
func (r *Runner) Command(name string, args ...string) *exec.Cmd {
	return r.cmd(name, args...)
}
