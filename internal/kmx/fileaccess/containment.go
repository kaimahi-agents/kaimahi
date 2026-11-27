package fileaccess

import (
	"context"
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

func lexicalContains(tree, candidate string) bool {
	root, rootErr := filepath.Abs(tree)
	absolute, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return true
	}
	relative, err := filepath.Rel(root, absolute)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
