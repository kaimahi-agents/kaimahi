//go:build !windows

package app

import "golang.org/x/sys/unix"

func markdownTestFIFO(path string) error { return unix.Mkfifo(path, 0600) }
