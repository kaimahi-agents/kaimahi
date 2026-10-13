package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestCreateSuiteMemberTerminalConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
		create, noPrompt     bool
	}{
		{"default", "\n", "", true, false},
		{"yes", "yes\n", "", true, false},
		{"upper yes", "Y\n", "", true, false},
		{"no", "no\n", "cancelled", false, false},
		{"EOF", "\x04", "confirmation", false, false},
		{"whitespace EOF", " \x04\x04", "confirmation", false, false},
		{"affirmative EOF", "y\x04\x04", "confirmation", false, false},
		{"invalid", "perhaps\n", "confirmation", false, false},
		{"cancel before", "", "", false, true},
		{"cancel after", "y\n", "", false, false},
		{"hidden terminal", "", "--workspace", false, true},
		{"missing model", "", "--model and --instructions", false, true},
		{"invalid model", "", "exact model", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.name == "upper yes" {
				root = filepath.Join(root, "unsafe\n\x1b[31m\u202e")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			input := filepath.Join(t.TempDir(), "input")
			writeFile(t, input, "Answer briefly.\n")
			opt := workspace.CreateOptions{Name: "member", Model: "exact-model", InstructionsFile: input}
			master, slave := chatPTY(t, 100)
			var out, hidden bytes.Buffer
			a := &App{Out: &out, Err: slave, Stdin: slave}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch tc.name {
			case "cancel before":
				cancel()
			case "hidden terminal":
				a.Err = &hidden
			case "missing model":
				opt.Model = ""
			case "invalid model":
				opt.Model = " model "
			}
			done := make(chan error, 1)
			go func() { done <- a.CreateSuiteMember(ctx, "", opt) }()
			if !tc.noPrompt {
				var captured strings.Builder
				chatPTYReadUntil(t, master, &captured, func(s string) bool { return strings.Contains(s, "Start one here? [Y/n]") })
				if tc.name == "upper yes" {
					if !strings.Contains(captured.String(), `unsafe\n\x1b[31m\u202e`) || strings.ContainsAny(captured.String(), "\n\r\x1b\u202e") {
						t.Fatalf("unsafe cwd was not escaped to one line: %q", captured.String())
					}
				} else if !strings.Contains(captured.String(), "No workspace found in "+root) {
					t.Fatalf("prompt omits cwd: %q", captured.String())
				}
				if tc.name == "cancel after" {
					cancel()
				}
				_, _ = io.WriteString(master, tc.response)
			}
			select {
			case err := <-done:
				if strings.HasPrefix(tc.name, "cancel") {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation = %v", err)
					}
				} else if tc.want != "" {
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("error = %v, want %q", err, tc.want)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("confirmation did not finish")
			}
			if tc.name == "upper yes" {
				if !strings.Contains(out.String(), `unsafe\n\x1b[31m\u202e`) || strings.Count(out.String(), "\n") != 1 || strings.ContainsAny(out.String(), "\r\x1b\u202e") {
					t.Fatalf("unsafe workspace was not escaped to one status line: %q", out.String())
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil || (len(entries) != 0) != tc.create || (out.Len() != 0) != tc.create || hidden.Len() != 0 {
				t.Fatalf("wrong filesystem/output effects: %v, %v, %q, %q", entries, err, &out, &hidden)
			}
		})
	}
}
