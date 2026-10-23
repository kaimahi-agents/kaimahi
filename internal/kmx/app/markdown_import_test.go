package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownImportNewDefaultAndExplicitReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{true: "replace", false: "new"}[replace], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "frogagent.agent.md")
			original := []byte("You are a frog.\r\nRibbit.\r\n")
			if err := os.WriteFile(path, original, 0644); err != nil {
				t.Fatal(err)
			}
			input := "\n"
			if replace {
				input = "r\ny\n"
			}
			var output bytes.Buffer
			saved, err := ImportMarkdownAgent(t.Context(), path, strings.NewReader(input), &output)
			if err != nil {
				t.Fatal(err)
			}
			if replace && saved != path || !replace && saved == path {
				t.Fatalf("saved=%s", saved)
			}
			if !replace && strings.Count(output.String(), "]: ") != 1 {
				t.Fatalf("default import asked more than once: %s", &output)
			}
			b, err := os.ReadFile(saved)
			if err != nil || !bytes.HasSuffix(b, original) {
				t.Fatal("instructions changed")
			}
			a, err := ReadMarkdownAgent(saved)
			if err != nil || a.Name != "frogagent" || a.Description != "frogagent agent" {
				t.Fatalf("agent=%+v err=%v", a, err)
			}
			if !replace {
				b, _ := os.ReadFile(path)
				if !bytes.Equal(b, original) {
					t.Fatal("original overwritten")
				}
			}
		})
	}
}

func TestMarkdownImportCancellationAndCollisionPreserveOriginal(t *testing.T) {
	for _, input := range []string{"n\n", "", "r\n\n", "\n"} {
		path := filepath.Join(t.TempDir(), "frog.agent.md")
		original := []byte("Be a frog")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		collision := strings.TrimSuffix(path, ".agent.md") + ".imported.agent.md"
		if err := os.WriteFile(collision, []byte("existing user content"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportMarkdownAgent(t.Context(), path, strings.NewReader(input), io.Discard); err == nil {
			t.Fatal("unexpected import success")
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, original) {
			t.Fatal("original modified")
		}
		got, _ = os.ReadFile(collision)
		if string(got) != "existing user content" {
			t.Fatal("collision overwritten")
		}
	}
}
