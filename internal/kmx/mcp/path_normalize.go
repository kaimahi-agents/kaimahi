package mcp

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

func hostAgentPath(p string) (string, error) {
	return normalizeAgentPath(p, runtime.GOOS, "/mnt")
}

// Translate host Windows names before Unix Abs can mistake them for cwd-relative
// filenames. The mount base is internal; callers cannot select a different mapping.
func normalizeAgentPath(p, goos, mountBase string) (string, error) {
	if goos == "windows" {
		return p, nil
	}
	if strings.HasPrefix(p, `\\`) {
		return "", refusal("unsafe_path")
	}
	// The builder sends WSL-form paths too; neither spelling may invent a drive.
	if goos == "linux" && filepath.IsAbs(p) {
		relative, err := filepath.Rel(mountBase, p)
		if err == nil && filepath.IsLocal(relative) {
			if driveLetter(relative[0]) && (len(relative) == 1 || relative[1] == filepath.Separator) {
				if err := requireDrive(filepath.Join(mountBase, relative[:1])); err != nil {
					return "", err
				}
			}
			return p, nil
		}
	}
	if len(p) < 2 || p[1] != ':' || !driveLetter(p[0]) {
		return p, nil
	}
	if goos != "linux" || len(p) < 3 || p[2] != '\\' && p[2] != '/' {
		return "", refusal("unsafe_path")
	}
	mount := filepath.Join(mountBase, strings.ToLower(p[:1]))
	if err := requireDrive(mount); err != nil {
		return "", err
	}
	// Clean within the Windows volume: C:\..\x remains C:\x, not /mnt/x.
	suffix := path.Clean("/" + strings.ReplaceAll(p[2:], `\`, "/"))
	return filepath.Join(mount, filepath.FromSlash(strings.TrimPrefix(suffix, "/"))), nil
}

func driveLetter(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func requireDrive(mount string) error {
	info, err := os.Stat(mount)
	if err != nil || !info.IsDir() {
		return refusal("unsafe_path")
	}
	return nil
}
