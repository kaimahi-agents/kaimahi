//go:build !windows

package app

import (
	"crypto/sha256"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Keep the existing Unix partition identity so retained records remain
// discoverable after this storage integration.
func suiteFailureWorkspaceKey(workspaceRoot, _ string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceRoot)))
}

func suiteFailureStateDir(bool) (string, error) {
	return config.StateDir()
}
