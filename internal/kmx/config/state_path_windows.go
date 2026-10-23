//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func stateDirPath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("KMX_HOME")); home != "" {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("KMX_HOME is not an absolute path")
		}
		return home, nil
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		known, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DONT_VERIFY)
		if err != nil {
			return "", fmt.Errorf("cannot locate LocalAppData (set KMX_HOME): %w", err)
		}
		local = known
	}
	if !filepath.IsAbs(local) {
		return "", fmt.Errorf("LOCALAPPDATA is not an absolute path (set KMX_HOME)")
	}
	return filepath.Join(local, "kmx"), nil
}
