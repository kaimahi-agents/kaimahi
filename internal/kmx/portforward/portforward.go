// Package portforward owns loopback kubectl port-forwards and their bind proof.
package portforward

import (
	"bytes"
	"fmt"
	"os/exec"
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
)

// Kube prepares a kubectl command that the forward manages itself. The caller
// supplies cluster context and, when needed, cancellation through the command.
type Kube interface {
	Command(args ...string) *exec.Cmd
}

// Forward is a `kubectl port-forward` kmx started and proved is its own.
// Callers must not send a byte until kubectl has said it bound the requested
// port on 127.0.0.1. Ownership does not prove a later connection to a reused port.
type Forward struct {
	Port string
	pf   *exec.Cmd
	log  *syncBuffer
	// done closes when the forward's process exits, so a forward that dies
	// immediately (the port is taken, the deployment is missing) is a
	// failure in milliseconds rather than a 30-second wait.
	done chan struct{}
}

// Start opens a forward to one target and waits until kubectl reports the bind.
//
// Both halves of that wait are load-bearing. `--address 127.0.0.1` makes
// kubectl FAIL when the port is taken rather than bind only the v6 side, and
// kubectl's own "Forwarding from" line is the only proof that the socket we
// are about to talk to is OURS. Probing the service behind it first would
// accept a 200 from a stale forward to a DIFFERENT cluster.
func Start(k Kube, namespace, target, localPort, remotePort string) (*Forward, error) {
	f := &Forward{Port: localPort, log: &syncBuffer{}}
	f.pf = k.Command("-n", namespace, "port-forward", "--address", "127.0.0.1",
		target, localPort+":"+remotePort)
	// kubectl announces the bind on stdout and its failures on stderr; both
	// are evidence, so both are kept.
	f.pf.Stdout, f.pf.Stderr = f.log, f.log
	if err := f.pf.Start(); err != nil {
		return nil, fmt.Errorf("cannot port-forward to %s in %s: %w", target, namespace, err)
	}
	f.done = make(chan struct{})
	go func() {
		_ = f.pf.Wait()
		close(f.done)
	}()

	want := "Forwarding from 127.0.0.1:" + localPort
	bound := run.Poll(pollAttempts, pollInterval, func() bool {
		if strings.Contains(f.log.String(), want) {
			return true
		}
		select {
		case <-f.done:
			return true // it exited; the check below reports the failure
		default:
			return false
		}
	})
	if !bound || !strings.Contains(f.log.String(), want) {
		f.Close()
		return nil, fmt.Errorf("the port-forward to %s never came up on 127.0.0.1:%s:\n  %s\n"+
			"  Refusing to continue: if another cluster's forward holds this port, the\n"+
			"  operation would have landed THERE. Use a free port.",
			target, localPort, f.Detail())
	}
	return f, nil
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

// Close tears the forward down.
func (f *Forward) Close() {
	if f == nil || f.pf == nil || f.pf.Process == nil {
		return
	}
	_ = f.pf.Process.Kill()
	<-f.done
}

// syncBuffer collects the forward's output while its goroutine writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
