package app

import (
	"context"
	"fmt"
	"strings"
)

// Tool discovery shares the checked GET transport, never mutation captures.
func (a *App) toolRead(ctx context.Context, path string) ([]byte, string) {
	var raw []byte
	var failure *discoveryReadFailure
	if a.discoveryRead != nil {
		raw, failure = a.discoveryRead.capture(ctx, a, "get", "--raw", path)
	} else {
		raw, failure = a.discoveryCapture(ctx, a.Cfg.KubeContext, "get", "--raw", path)
	}
	if failure == nil {
		return raw, ""
	}
	if failure.safeCode() == "cancelled" || failure.safeCode() == "deadline_exceeded" {
		return nil, "cancelled"
	}
	switch failure.reason {
	case "read_denied":
		return nil, "read_denied"
	case "unauthorized":
		return nil, "authentication_required"
	case "resource_missing":
		if failure.exactToolMissing != "" && strings.HasSuffix(path, "/tools/"+failure.exactToolMissing) {
			return nil, "resource_missing"
		}
	}
	return nil, "read_unknown"
}
func toolDiscoveryError(reason string) error {
	return fmt.Errorf("Orka tool inventory is incomplete (%s)", reason)
}
