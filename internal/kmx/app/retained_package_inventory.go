package app

import (
	"context"
	"errors"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func retainedRootName(name string) bool {
	switch name {
	case "control.json", "use.lock", "ready.json", "released.json", "build", "layout":
		return true
	}
	return strings.HasPrefix(name, ".kmx-layout-") && strings.HasSuffix(name, ".lock")
}

// Each scan opens a fresh anchored tree; directory offsets never become a
// purported complete inventory. Bounds include directories and all owned bytes.
func retainedInventory(ctx context.Context, path string, expected *fileaccess.Directory, flush bool) (size int64, err error) {
	root, err := fileaccess.OpenDirectory(ctx, path)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err = retainedSame(ctx, expected, root); err != nil {
		return 0, err
	}
	count := 0
	var walk func(*fileaccess.Directory, int) error
	walk = func(dir *fileaccess.Directory, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 16 {
			return errRetainedUnavailable
		}
		entries, err := dir.ReadDir(1025 - count)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 1024 || depth == 0 && !retainedRootName(entry.Name()) {
				return errRetainedUnavailable
			}
			if entry.IsDir() {
				child, err := fileaccess.OpenDirectoryAt(ctx, dir, entry.Name())
				if err != nil {
					return err
				}
				err = fileaccess.CheckPrivate(ctx, child, true)
				if err == nil {
					err = walk(child, depth+1)
				}
				err = errors.Join(err, child.Close())
				if err != nil {
					return err
				}
				continue
			}
			f, err := fileaccess.OpenRegularAt(ctx, dir, entry.Name())
			if err != nil {
				return err
			}
			info, statErr := f.Stat()
			if statErr != nil {
				f.Close()
				return statErr
			}
			size += info.Size()
			if size > retainedEntryBytes-retainedMetadataBytes {
				f.Close()
				return errRetainedCapacity
			}
			links, linkErr := fileaccess.LinkCount(ctx, f)
			if linkErr != nil || links != 1 {
				f.Close()
				return errRetainedUnavailable
			}
			privateErr := fileaccess.CheckPrivate(ctx, f, false)
			if privateErr == nil && flush {
				privateErr = fileaccess.SyncFile(ctx, f)
			}
			if err = errors.Join(privateErr, f.Close()); err != nil {
				return err
			}
		}
		if flush {
			return fileaccess.SyncDirectory(ctx, dir)
		}
		return nil
	}
	err = walk(root, 0)
	if err != nil {
		return size, err
	}
	return size, retainedSame(ctx, expected, root)
}
