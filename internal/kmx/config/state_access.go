package config

import (
	"context"
	"errors"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

// PrepareStateDir resolves and creates the private state root, or validates an
// existing one. It repairs and adopts nothing.
func PrepareStateDir() (string, error) {
	dir, err := stateDirPath()
	if err != nil {
		return "", err
	}
	return prepareStateDir(context.Background(), dir)
}

func prepareStateDir(ctx context.Context, dir string) (string, error) {
	opened, err := fileaccess.EnsurePrivateDirectory(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := opened.Close(); err != nil {
		return "", err
	}
	return dir, nil
}

// ExistingStateDir resolves and validates an existing private state root
// without creating any directory or file.
func ExistingStateDir() (string, error) {
	dir, err := stateDirPath()
	if err != nil {
		return "", err
	}
	return existingStateDir(context.Background(), dir)
}

func existingStateDir(ctx context.Context, dir string) (string, error) {
	opened, err := fileaccess.OpenPrivateDirectory(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := errors.Join(fileaccess.CheckPrivate(ctx, opened, true), opened.Close()); err != nil {
		return "", err
	}
	return dir, nil
}
