//go:build !windows

package oras

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Local OCI stores keep separate in-memory indexes, so their instance locks
// cannot prevent cross-process lost updates. Exclusion must precede store open.
func lockLayoutPublication(ctx context.Context, destination string) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	identity := sha256.Sum256([]byte(filepath.Join(parent, filepath.Base(absolute))))
	lock := filepath.Join(parent, fmt.Sprintf(".kmx-layout-%x.lock", identity))
	if err := os.Mkdir(lock, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("another local layout publisher may be active; inspect %s and remove it only after verifying no publisher is running", lock)
		}
		return nil, fmt.Errorf("acquire local layout writer exclusion: %w", err)
	}
	return func() error { return os.Remove(lock) }, nil
}
