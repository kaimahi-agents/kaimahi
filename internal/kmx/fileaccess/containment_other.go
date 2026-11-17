//go:build !windows

package fileaccess

import "context"

func physicallyContains(_ context.Context, tree, candidate string) (bool, error) {
	return lexicalContains(tree, candidate), nil
}
