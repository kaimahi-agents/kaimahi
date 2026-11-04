//go:build !unix

package localtools

import "context"

// Native command tools are outside the qualified Windows CLI boundary.
func runCommand(context.Context, CommandConfig, func(...string) error) (map[string]any, error) {
	return nil, ErrCommand
}
