//go:build !windows

package app

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"golang.org/x/sys/unix"
)

func retainedPlatform() error { return nil }

// Only the allocating invocation calls this, after its exclusive, create-only
// compiler/copy. Reopened entries never gain a permission-repair path. The root
// and each opened object must still be the observed owned object before chmod.
func retainedFinalize(ctx context.Context, path string, expected *fileaccess.Directory) (err error) {
	checked, err := fileaccess.OpenDirectory(ctx, path)
	if err != nil {
		return err
	}
	if err = errors.Join(retainedSame(ctx, expected, checked), checked.Close()); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return errRetainedUnavailable
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	wanted, err := expected.Stat()
	if err != nil {
		return err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(wanted, actual) {
		return errRetainedUnavailable
	}
	count := 0
	var size int64
	var walk func(string, int) error
	walk = func(name string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 1024 || depth > 16 {
			return errRetainedUnavailable
		}
		before, err := root.Lstat(name)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || (!before.IsDir() && !before.Mode().IsRegular()) {
			return errRetainedUnavailable
		}
		f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return errRetainedUnavailable
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(before, opened) {
			f.Close()
			return errRetainedUnavailable
		}
		stat, ok := opened.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || !opened.IsDir() && stat.Nlink != 1 {
			f.Close()
			return errRetainedUnavailable
		}
		mode := os.FileMode(0600)
		if opened.IsDir() {
			mode = 0700
		} else {
			size += opened.Size()
			if size > retainedEntryBytes-retainedMetadataBytes {
				f.Close()
				return errRetainedCapacity
			}
		}
		if err = f.Chmod(mode); err != nil {
			f.Close()
			return errRetainedUnavailable
		}
		if !opened.IsDir() {
			return f.Close()
		}
		children, err := f.ReadDir(1025 - count)
		if errors.Is(err, io.EOF) {
			err = nil
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return errRetainedUnavailable
		}
		for _, child := range children {
			if name == "." && !retainedRootName(child.Name()) {
				return errRetainedUnavailable
			}
			rel := child.Name()
			if name != "." {
				rel = name + "/" + rel
			}
			if err := walk(rel, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(".", 0)
}
