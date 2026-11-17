package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// PhysicallyContains reports whether candidate's nearest existing object is the
// same as or below tree. It collapses ordinary NTFS aliases before comparison.
func PhysicallyContains(ctx context.Context, tree, candidate string) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, wrapAccess("compare physical containment", err)
	}
	return physicallyContains(ctx, tree, candidate)
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

func lexicalContains(tree, candidate string) bool {
	root, rootErr := filepath.Abs(tree)
	absolute, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return true
	}
	relative, err := filepath.Rel(root, absolute)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
