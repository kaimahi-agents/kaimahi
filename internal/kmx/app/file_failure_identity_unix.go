//go:build !windows && !darwin

package app

import "path/filepath"

func fileFailureLeaf(name string) string { return filepath.Base(name) }
