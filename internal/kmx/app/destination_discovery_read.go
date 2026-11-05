package app

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

type discoveryReadReason string

var errDiscoveryLimit = errors.New("bounded discovery capture overflow")

type discoveryReadFailure struct {
	reason           discoveryReadReason
	cause            error
	exactToolMissing string
}

func (e *discoveryReadFailure) Error() string { return "Destination discovery is incomplete." }
func (e *discoveryReadFailure) Unwrap() error { return e.cause }
func (e *discoveryReadFailure) safeCode() string {
	switch {
	case errors.Is(e.cause, context.Canceled):
		return "cancelled"
	case errors.Is(e.cause, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	switch e.reason {
	case "read_denied":
		return "permission_denied"
	case "unauthorized":
		return "credential_unavailable"
	case "network_unavailable", "resource_missing":
		return "dependency_unavailable"
	default:
		return "operation_failed"
	}
}

// classifyDiscoveryRead recognizes diagnostic hints, never authenticated API proof.
// This fixed grammar is separate from legacy capture and never exposes suffixes.
func classifyDiscoveryRead(stderr []byte, truncated bool) discoveryReadReason {
	if truncated || !utf8.Valid(stderr) {
		return "read_unknown"
	}
	found := discoveryReadReason("")
	for _, line := range strings.Split(string(stderr), "\n") {
		line = strings.TrimSuffix(line, "\r")
		reason := discoveryReadReason("")
		for _, marker := range []struct {
			prefix string
			reason discoveryReadReason
		}{
			{"Error from server (Forbidden)", "read_denied"},
			{"Error from server (NotFound)", "resource_missing"},
			{"error: You must be logged in to the server", "unauthorized"},
			{"Unable to connect", "network_unavailable"},
		} {
			if !strings.HasPrefix(line, marker.prefix) {
				continue
			}
			rest := strings.TrimPrefix(line, marker.prefix)
			if rest != "" {
				r, _ := utf8.DecodeRuneInString(rest)
				if r != ':' && !unicode.IsSpace(r) {
					continue
				}
			}
			reason = marker.reason
		}
		if reason == "" && strings.HasPrefix(line, "Error from server (") {
			return "read_unknown"
		}
		if reason != "" {
			if found != "" && found != reason {
				return "read_unknown"
			}
			found = reason
		}
	}
	if found == "" {
		return "read_unknown"
	}
	return found
}

// Named buffer prevents promoted WriteString/ReadFrom from bypassing the bound.
type discoveryBuffer struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (b *discoveryBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *discoveryBuffer) Len() int      { return b.buffer.Len() }
func (b *discoveryBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.overflow = true
		return 0, errors.New("discovery capture exceeds bound")
	}
	b.remaining -= len(p)
	return b.buffer.Write(p)
}

func (a *App) discoveryCapture(ctx context.Context, selectedContext string, args ...string) ([]byte, *discoveryReadFailure) {
	raw, failure, _ := a.discoveryCaptureBudget(ctx, selectedContext, 4<<20, args...)
	return raw, failure
}

func (a *App) discoveryCaptureBudget(ctx context.Context, selectedContext string, maxStdout int, args ...string) (raw []byte, failure *discoveryReadFailure, captured int) {
	if err := ctx.Err(); err != nil {
		return nil, &discoveryReadFailure{reason: "read_unknown", cause: err}, 0
	}
	if maxStdout <= 0 {
		return nil, &discoveryReadFailure{reason: "read_unknown"}, 0
	}
	child, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	runner := *a.Run
	runner.Context = child
	runner.Echo = false
	runner.Env = nil
	for _, entry := range a.Run.Env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "KUBERC" && key != "KUBECTL_KUBERC" {
			runner.Env = append(runner.Env, entry)
		}
	}
	runner.Unset = append(append([]string(nil), a.Run.Unset...), "KUBERC", "KUBECTL_KUBERC")
	runner.Env = append(runner.Env, "KUBERC=off", "KUBECTL_KUBERC=false")
	argv := []string{"--request-timeout=10s"}
	if selectedContext != "" {
		argv = append([]string{"--context", selectedContext}, argv...)
	}
	argv = append(argv, args...)
	cmd := runner.Command("kubectl", argv...)
	cmd.WaitDelay = time.Second
	out, diagnostics := &discoveryBuffer{remaining: min(maxStdout, 4<<20)}, &discoveryBuffer{remaining: 4 << 10}
	defer func() { captured = out.Len() }()
	cmd.Stdout, cmd.Stderr = out, diagnostics
	err := a.executeCapture(child, cmd)
	if child.Err() != nil {
		return nil, &discoveryReadFailure{reason: "read_unknown", cause: child.Err()}, 0
	}
	if out.overflow || diagnostics.overflow {
		return nil, &discoveryReadFailure{reason: "read_unknown", cause: errDiscoveryLimit}, 0
	}
	if err != nil {
		// A foreground-group interrupt can reap kubectl before NotifyContext's
		// goroutine runs. Trust the kernel's signal status, never an exit-code
		// convention or stderr text; SIGKILL and voluntary exit 130 stay unknown.
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() && (status.Signal() == syscall.SIGINT || status.Signal() == syscall.SIGTERM) {
				return nil, &discoveryReadFailure{reason: "read_unknown", cause: context.Canceled}, 0
			}
		}
		failure = &discoveryReadFailure{reason: classifyDiscoveryRead(diagnostics.Bytes(), false), cause: err}
		if failure.reason == "resource_missing" && len(args) == 3 && args[0] == "get" && args[1] == "--raw" {
			const prefix = "/apis/core.orka.ai/v1alpha1/namespaces/"
			if strings.HasPrefix(args[2], prefix) {
				parts := strings.Split(strings.TrimPrefix(args[2], prefix), "/")
				if len(parts) == 3 && parts[1] == "tools" && strings.TrimSpace(string(diagnostics.Bytes())) == "Error from server (NotFound): tools.core.orka.ai "+strconv.Quote(parts[2])+" not found" {
					failure.exactToolMissing = parts[2]
				}
			}
		}
		return out.Bytes(), failure, 0
	}
	if diagnostics.Len() != 0 {
		return out.Bytes(), &discoveryReadFailure{reason: "read_unknown", cause: errors.New("unexpected diagnostics")}, 0
	}
	return out.Bytes(), nil, 0
}
