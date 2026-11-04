package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type discoveryReadReason string

type discoveryReadFailure struct {
	reason discoveryReadReason
	cause  error
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
	if err := ctx.Err(); err != nil {
		return nil, &discoveryReadFailure{"read_unknown", err}
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
	out, diagnostics := &discoveryBuffer{remaining: 4 << 20}, &discoveryBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, diagnostics
	err := a.executeCapture(child, cmd)
	if child.Err() != nil {
		return nil, &discoveryReadFailure{"read_unknown", child.Err()}
	}
	if out.overflow || diagnostics.overflow {
		return nil, &discoveryReadFailure{"read_unknown", errors.New("bounded capture overflow")}
	}
	if err != nil {
		return out.Bytes(), &discoveryReadFailure{classifyDiscoveryRead(diagnostics.Bytes(), false), err}
	}
	if diagnostics.Len() != 0 {
		return out.Bytes(), &discoveryReadFailure{"read_unknown", errors.New("unexpected diagnostics")}
	}
	return out.Bytes(), nil
}
