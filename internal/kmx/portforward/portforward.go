// Package portforward owns loopback kubectl port-forwards and their bind proof.
package portforward

import (
	"bytes"
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
)

// Kube prepares a kubectl command that the forward manages itself. The caller
// supplies cluster context and, when needed, cancellation through the command.
type Kube interface {
	Command(args ...string) *exec.Cmd
}

// Forward is a `kubectl port-forward` kmx started and proved is its own.
// Callers must not send a byte until kubectl has said it bound the selected
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

	bound := run.Poll(pollAttempts, pollInterval, func() bool {
		if port := announcedPort(f.log.String(), localPort); port != "" {
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
		f.Close()
		requested := localPort
		if requested == "" {
			requested = "<automatic port>"
		}
		return nil, fmt.Errorf("the port-forward to %s never came up on 127.0.0.1:%s:\n  %s\n"+
			"  Refusing to continue: if another cluster's forward holds this port, the\n"+
			"  operation would have landed THERE. Use a free port.",
			target, requested, f.Detail())
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
