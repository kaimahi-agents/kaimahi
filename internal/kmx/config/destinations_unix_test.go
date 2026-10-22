//go:build !windows

package config

import (
	"testing"

	"golang.org/x/sys/unix"
)

func testFIFO(t *testing.T, path string) {
	t.Helper()
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
}
