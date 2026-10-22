//go:build !windows

package toolchain

import (
	"os"
	"path/filepath"
)

// install writes the binary atomically: a killed download must not leave a
// truncated file behind under the real name.
func install(path string, binary []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// relink points linkDir/<name> at the versioned cache entry, replacing
// whatever was there. Replacing rather than keeping is the point: after a pin
// bump the old link would otherwise still be the thing on PATH.
func relink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, link)
}
