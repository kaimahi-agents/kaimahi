//go:build !windows

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSquadRejectsNativeSpecialAndLinkedFiles(t *testing.T) {
	for _, kind := range []string{"fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			target := filepath.Join(root, ".squad/unsafe")
			if kind == "fifo" {
				if err := syscall.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				source := filepath.Join(t.TempDir(), "source")
				os.WriteFile(source, []byte("do not touch"), 0644)
				if err := os.Link(source, target); err != nil {
					t.Skip("host does not support hardlinks")
				}
			}
			c, err := OpenSquad(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err == nil {
				t.Fatal("special/linked native target accepted")
			}
		})
	}
}
