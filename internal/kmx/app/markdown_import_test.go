package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownDirectoryImportPreservesSourceAndCreatesTemplates(t *testing.T) {
	for _, source := range []string{"You are a frog.\r\nRibbit.\r\n", markdownTestAgent} {
		path := filepath.Join(t.TempDir(), "frog.agent.md")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(t.TempDir(), "frog")
		saved, err := ImportMarkdownDirectory(t.Context(), path, dest)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, saved} {
			b, err := os.ReadFile(p)
			if err != nil || !bytes.Equal(b, []byte(source)) {
				t.Fatal("source rewritten", err)
			}
		}
		a, err := ReadMarkdownAgent(dest)
		if err != nil || len(a.Examples) != 0 || len(a.CLITools) != 0 {
			t.Fatal("templates invalid", err)
		}
		for _, p := range []string{MarkdownToolsPath(saved), MarkdownTestsPath(saved)} {
			if _, err := os.Stat(p); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ImportMarkdownDirectory(t.Context(), path, dest); err == nil {
			t.Fatal("existing destination overwritten")
		}
	}
}

func TestMarkdownImportCancellationPreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frog.agent.md")
	if err := os.WriteFile(path, []byte("Be a frog"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportMarkdownAgent(t.Context(), path, strings.NewReader("n\n"), io.Discard); err == nil {
		t.Fatal("decline imported")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ImportMarkdownDirectory(ctx, path, markdownStem(path)); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	if _, err := os.Stat(markdownStem(path)); !os.IsNotExist(err) {
		t.Fatal("cancel created directory")
	}
}
