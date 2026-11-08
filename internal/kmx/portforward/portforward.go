// Package portforward manages owned loopback kubectl port-forwards. Commands
// retain the platform process-tree lifecycle, and Close reports uncertain cleanup.
// Callers must wait for the forward's bind announcement before sending data;
// ownership does not authenticate later connections to a reused port.
package portforward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The bind readiness wait: 150 × 0.2s. Variables rather than constants so
// tests can exercise timeout paths without spending half a minute on each.
var (
	pollAttempts = 150
	pollInterval = 200 * time.Millisecond
	cleanupWait  = 5 * time.Second
)

const forwardLogLimit = 64 << 10

// Kube prepares a kubectl command that the forward manages itself. The caller
// supplies cluster context and, when needed, cancellation through the command.
type Kube interface {
	OwnedCommand(args ...string) (*run.Command, error)
}

// Forward is a `kubectl port-forward` kmx started and proved is its own.
// Callers must not send a byte until kubectl has said it bound the selected
// port on 127.0.0.1. Ownership does not prove a later connection to a reused port.
type Forward struct {
	Port string
	pf   *run.Command
	log  *syncBuffer
	wait chan error
	stop sync.Once
	err  error
	// done closes when the forward's process exits, so a forward that dies
	// immediately (the port is taken, the deployment is missing) is a
	// failure in milliseconds rather than a 30-second wait.
	done chan struct{}
}

// Start opens a forward to one target and waits until kubectl reports the bind.
// An empty localPort lets kubectl allocate and retain a free port; Port contains
// the actual bound port on return. There is no reserve-close-rebind window.
//
// Both halves of that wait are load-bearing. `--address 127.0.0.1` makes
// kubectl FAIL when the port is taken rather than bind only the v6 side, and
// kubectl's own "Forwarding from" line is the only proof that the socket we
// are about to talk to is OURS. Probing the service behind it first would
// accept a 200 from a stale forward to a DIFFERENT cluster.
func Start(k Kube, namespace, target, localPort, remotePort string) (*Forward, error) {
	f := &Forward{log: &syncBuffer{}}
	var err error
	f.pf, err = k.OwnedCommand("-n", namespace, "port-forward", "--address", "127.0.0.1",
		target, localPort+":"+remotePort)
	if err != nil {
		return nil, fmt.Errorf("cannot prepare port-forward to %s in %s: %w", target, namespace, err)
	}
	// kubectl announces the bind on stdout and its failures on stderr; both
	// are evidence, so both are kept.
	f.pf.Stdout, f.pf.Stderr = f.log, f.log
	if err := f.pf.Start(); err != nil {
		return nil, fmt.Errorf("cannot port-forward to %s in %s: %w", target, namespace, err)
	}
	f.done = make(chan struct{})
	f.wait = make(chan error, 1)
	go func() {
		f.wait <- f.pf.Wait()
		close(f.done)
	}()

	bound := run.Poll(pollAttempts, pollInterval, func() bool {
		if port := announcedPort(f.log.bindEvidence(), localPort); port != "" {
			f.Port = port
			return true
		}
		select {
		case <-f.done:
			return true // it exited; the check below reports the failure
		default:
			return false
		}
	})
	if !bound || f.Port == "" {
		closeErr := f.Close()
		requested := localPort
		if requested == "" {
			requested = "<automatic port>"
		}
		return nil, errors.Join(fmt.Errorf("the port-forward to %s never came up on 127.0.0.1:%s:\n  %s\n"+
			"  Refusing to continue: if another cluster's forward holds this port, the\n"+
			"  operation would have landed THERE. Use a free port.",
			target, requested, f.Detail()), closeErr)
	}
	return f, nil
}

// announcedPort accepts only a complete IPv4 bind announcement for this forward.
// kubectl resolves Service ports to Pod target ports, so the announced remote
// number can differ from the request. A partial write or prefix is not bind proof.
func announcedPort(output, requested string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines[:len(lines)-1] {
		address, ok := strings.CutPrefix(line, "Forwarding from 127.0.0.1:")
		if !ok {
			continue
		}
		port, remote, ok := strings.Cut(address, " -> ")
		n, err := strconv.Atoi(port)
		target, targetErr := strconv.Atoi(remote)
		if ok && err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port &&
			targetErr == nil && target > 0 && target <= 65535 && strconv.Itoa(target) == remote &&
			(requested == "" || port == requested) {
			return port
		}
	}
	return ""
}

// Detail is kubectl's own output, indented for a multi-line error.
func (f *Forward) Detail() string {
	out := strings.TrimSpace(f.log.String())
	if out == "" {
		out = "no output from kubectl"
	}
	return strings.ReplaceAll(out, "\n", "\n  ")
}

// Done closes when the owned forward process has exited. Callers that retain
// authenticated connections can use it to cancel work after losing the tunnel.
// It is not an authentication proof for a later connection to the same port.
func (f *Forward) Done() <-chan struct{} { return f.done }

// Close tears the forward down and positively waits for process exit.
func (f *Forward) Close() error {
	if f == nil || f.pf == nil || f.pf.Process == nil {
		return nil
	}
	f.stop.Do(func() {
		select {
		case <-f.done:
			f.err = normalizeForwardWait(<-f.wait)
		default:
			terminated := make(chan error, 1)
			go func() { terminated <- run.Terminate(f.pf) }()
			timer := time.NewTimer(cleanupWait)
			defer timer.Stop()
			var terminateErr error
			select {
			case terminateErr = <-terminated:
			case <-timer.C:
				f.err = fmt.Errorf("port-forward cleanup unconfirmed: termination did not return within %s", cleanupWait)
				return
			}
			select {
			case <-f.done:
				f.err = errors.Join(terminateErr, normalizeForwardWait(<-f.wait))
			case <-timer.C:
				f.err = errors.Join(terminateErr, fmt.Errorf("port-forward cleanup unconfirmed: process did not exit within %s", cleanupWait))
			}
		}
	})
	return f.err
}

func normalizeForwardWait(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var retained []error
		for _, part := range joined.Unwrap() {
			if normalized := normalizeForwardWait(part); normalized != nil {
				retained = append(retained, normalized)
			}
		}
		return errors.Join(retained...)
	}
	if _, ok := err.(*exec.ExitError); ok {
		return nil
	}
	// The forward's connection watcher can cancel the command context after
	// explicit termination starts. Done already proves process exit; retain any
	// separately joined platform cleanup error, but not that lifetime signal.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// syncBuffer collects the forward's output while its goroutine writes it.
type syncBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	accepted := 0
	if remaining := forwardLogLimit - b.buf.Len(); remaining > 0 {
		accepted = min(len(p), remaining)
		_, _ = b.buf.Write(p[:accepted])
	}
	if written > accepted {
		b.truncated = true
	}
	return written, nil
}

// bindEvidence returns only subprocess bytes. A synthetic diagnostic marker
// must never complete a truncated bind announcement.
func (b *syncBuffer) bindEvidence() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return b.buf.String() + "\n[output truncated]"
	}
	return b.buf.String()
}
