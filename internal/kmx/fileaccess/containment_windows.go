//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const containmentSeparator = `\`

func physicallyContains(ctx context.Context, tree, candidate string) (bool, error) {
	root, err := OpenDirectory(ctx, tree)
	if err != nil {
		return false, wrapAccess("compare physical containment", err)
	}
	defer root.Close()
	rootPath, err := canonicalObjectPath(ctx, root)
	if err != nil {
		return false, wrapAccess("compare physical containment", err)
	}
	existing, suffix, err := nearestExistingDirectory(ctx, candidate)
	if err != nil {
		return false, wrapAccess("compare physical containment", err)
	}
	defer existing.Close()
	existingPath, err := canonicalObjectPath(ctx, existing)
	if err != nil {
		return false, wrapAccess("compare physical containment", err)
	}
	candidatePath := existingPath
	for i := len(suffix) - 1; i >= 0; i-- {
		candidatePath += containmentSeparator + strings.ToLower(suffix[i])
	}
	return candidatePath == rootPath || strings.HasPrefix(candidatePath, rootPath+containmentSeparator), nil
}

func nearestExistingDirectory(ctx context.Context, candidate string) (*Directory, []string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return nil, nil, err
	}
	path := absolute
	var suffix []string
	for {
		directory, err := OpenDirectory(ctx, path)
		if err == nil {
			return directory, suffix, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil, nil, ErrMissing
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}
