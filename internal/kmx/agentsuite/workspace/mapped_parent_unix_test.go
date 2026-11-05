//go:build !windows

package workspace

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestCompileMappedUnsafeParent(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			parent := mappedParent(t)
			planted := filepath.Join(parent, "planted")
			witness := filepath.Join(mappedParent(t), "witness")
			if err := os.WriteFile(witness, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "symlink" {
				err = os.Symlink(witness, planted)
			} else {
				err = unix.Mkfifo(planted, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if p, err := CompileMapped(t.Context(), mappedFixture(), parent); err == nil {
				p.Close()
				t.Fatal("unsafe staging parent accepted")
			}
			if _, err := os.Lstat(planted); err != nil {
				t.Fatal("foreign object removed", err)
			}
			if b, err := os.ReadFile(witness); err != nil || string(b) != "keep" {
				t.Fatal("foreign target changed", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 {
				t.Fatal("refusal staged content", err)
			}
		})
	}
}
