//go:build windows

package fileaccess

import (
	"context"
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
