//go:build windows

package app

import (
	"crypto/sha256"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Windows aliases and case variants share one physical workspace partition.
func suiteFailureWorkspaceKey(_ string, physicalIdentity string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(physicalIdentity)))
}

func suiteFailureStateDir(create bool) (string, error) {
	if create {
		return config.PrepareStateDir()
	}
	return config.ExistingStateDir()
}
