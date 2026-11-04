package app

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// toolRead is deliberately local to GET-only discovery. Legacy mutation/result
// captures retain their existing behavior; no stderr body crosses this boundary.
func (a *App) toolRead(ctx context.Context, path string) ([]byte, string) {
	if ctx.Err() != nil {
		return nil, "cancelled"
	}
	child, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prepared := a.Command("--request-timeout=10s", "get", "--raw", path)
	cmd := exec.CommandContext(child, prepared.Path, prepared.Args[1:]...)
	cmd.Env = prepared.Env
	cmd.WaitDelay = time.Second
	out, diagnostics := &orkaBoundedBuffer{remaining: 4 << 20}, &orkaBoundedBuffer{remaining: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, diagnostics
	err := a.executeCapture(child, cmd)
	if child.Err() != nil {
		return nil, "cancelled"
	}
	if err != nil {
		// Match kubectl's anchored server reason, never arbitrary substrings or
		// echoed names. Exact Tool NotFound is the only absence classification.
		text := strings.TrimSpace(diagnostics.buffer.String())
		switch {
		case strings.HasPrefix(text, "Error from server (Forbidden): "):
			return nil, "read_denied"
		case strings.HasPrefix(text, "Error from server (Unauthorized): "):
			return nil, "authentication_required"
		}
		const prefix = "/apis/core.orka.ai/v1alpha1/namespaces/"
		if strings.HasPrefix(path, prefix) {
			parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
			if len(parts) == 3 && parts[1] == "tools" && text == "Error from server (NotFound): tools.core.orka.ai "+strconv.Quote(parts[2])+" not found" {
				return nil, "resource_missing"
			}
		}
		return nil, "read_unknown"
	}
	return out.buffer.Bytes(), ""
}

func toolDiscoveryError(reason string) error {
	return fmt.Errorf("Orka tool inventory is incomplete (%s)", reason)
}
