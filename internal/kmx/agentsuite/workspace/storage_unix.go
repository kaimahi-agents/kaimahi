//go:build !windows

package workspace

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func privateTree(root string, setMode bool) error {
	return filepath.WalkDir(root, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("cache symlinks are not supported")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return errors.New("cache contains nonregular file")
		}
		if d.IsDir() {
			if setMode {
				return os.Chmod(name, 0o700)
			}
			if info.Mode().Perm() != 0o700 {
				return errors.New("cache directories must be private (0700)")
			}
		}
		return nil
	})
}

func createSourceFiles(ctx context.Context, root string, files map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dirs := map[string]bool{"agents": true, "eval": true, "build-profiles": true}
	for name := range files {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	var topDirs []string
	for dir := range dirs {
		if !strings.Contains(dir, "/") {
			topDirs = append(topDirs, dir)
		}
	}
	root, err := safeAbsolute(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, name := range append([]string{"workspace.yaml", "agent.yaml", "agentsuite.json"}, topDirs...) {
		if _, err := r.Lstat(name); !os.IsNotExist(err) {
			return fmt.Errorf("refusing existing workspace/member/bundle path %s", name)
		}
	}
	stage, err := os.MkdirTemp(root, ".kmx-create-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := writeFiles(stage, files); err != nil {
		return err
	}
	var owned []string
	published := false
	defer func() {
		if !published {
			for _, name := range owned {
				_ = r.RemoveAll(name)
			}
		}
	}()
	for _, name := range slices.Sorted(maps.Keys(dirs)) {
		if err := r.Mkdir(name, 0o700); err != nil {
			return err
		}
		owned = append(owned, name)
	}
	for name := range files {
		if name == "workspace.yaml" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.Link(filepath.Base(stage)+"/"+name, name); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Link(filepath.Base(stage)+"/workspace.yaml", "workspace.yaml"); err != nil {
		return err
	}
	published = true
	return nil
}

func writeFiles(root string, files map[string][]byte) error {
	for name, b := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}
