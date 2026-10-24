//go:build windows

package app

import (
	"path/filepath"
	"strings"
)

func fileFailureLeaf(name string) string { return strings.ToLower(filepath.Base(name)) }
