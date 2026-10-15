package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func createPromptPTY(t *testing.T, raw bool) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if raw {
		state, err := term.MakeRaw(int(slave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = term.Restore(int(slave.Fd()), state) })
	}
	return master, slave
}

func TestCreatePromptsOnlyForMissingTerminalInputs(t *testing.T) {
	for _, tc := range []struct {
		name                                                        string
		flags                                                       []string
		input, model, want, agent                                   string
		command                                                     []string
		modelPrompt, filePrompt, create, raw, hidden, piped, redact bool
	}{
		{name: "bare", input: "exact-model\n{file}\ny\n", model: "exact-model", modelPrompt: true, filePrompt: true, create: true},
		{name: "noncanonical paste", input: "exact-model\n{file}\ny\n", model: "exact-model", modelPrompt: true, filePrompt: true, create: true, raw: true},
		{name: "model flag", flags: []string{"--model", "flag-model"}, input: "{file}\ny\n", model: "flag-model", filePrompt: true, create: true},
		{name: "instructions flag", flags: []string{"--instructions", "{file}"}, input: "exact-model\ny\n", model: "exact-model", modelPrompt: true, create: true},
		{name: "fully flagged", flags: []string{"--model", "flag-model", "--instructions", "{file}"}, input: "y\n", model: "flag-model", create: true},
		{name: "explicit workspace", flags: []string{"--workspace", "{root}"}, input: "exact-model\n{file}\n", model: "exact-model", modelPrompt: true, filePrompt: true, create: true},
		{name: "declined workspace", input: "exact-model\n{file}\nn\n", want: "cancelled", modelPrompt: true, filePrompt: true},
		{name: "blank model", input: "\n", want: "required", modelPrompt: true},
		{name: "blank instructions", input: "exact-model\n\n", want: "required", modelPrompt: true, filePrompt: true},
		{name: "model EOF", input: "\x04", want: "read", modelPrompt: true},
		{name: "instructions EOF", input: "exact-model\n\x04", want: "read", modelPrompt: true, filePrompt: true},
		{name: "instructions without enter", input: "exact-model\n{file}\x04\x04", want: "read", modelPrompt: true, filePrompt: true},
		{name: "missing file", input: "exact-model\nmissing-file\n", want: "no such file", modelPrompt: true, filePrompt: true},
		{name: "inexact model", input: " model \n{file}\n", want: "exact model", modelPrompt: true, filePrompt: true},
		{name: "explicit blank model", flags: []string{"--model", " "}, want: "--model and --instructions"},
		{name: "explicit blank instructions", flags: []string{"--instructions", ""}, want: "--model and --instructions"},
		{name: "invalid name", agent: "BadName", want: "name"},
		{name: "credential name", agent: "ghp_" + strings.Repeat("x", 36), want: "credential-shaped", redact: true},
		{name: "help", command: []string{"create", "--help"}},
		{name: "completion", command: []string{"__complete", "create", ""}},
		{name: "hidden stderr", want: "--model and --instructions", hidden: true},
		{name: "piped stdin", input: "ignored-model\nignored-file\n", want: "--model and --instructions", piped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			input := filepath.Join(t.TempDir(), "instructions with spaces.md")
			instructions := []byte("Answer briefly.\n")
			if err := os.WriteFile(input, instructions, 0600); err != nil {
				t.Fatal(err)
			}
			master, slave := createPromptPTY(t, tc.raw)
			var out, hidden bytes.Buffer
			deps, loads := testDependencies(&out, &hidden)
			deps.stdin, deps.stderr = slave, slave
			if tc.hidden {
				deps.stderr = &hidden
			}
			var pipe *os.File
			if tc.piped {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { reader.Close() })
				if _, err := io.WriteString(writer, tc.input); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				deps.stdin, pipe = reader, reader
			}
			args := []string{"create", "greeter"}
			if tc.agent != "" {
				args[1] = tc.agent
			}
			if tc.command != nil {
				args = tc.command
			}
			for _, flag := range tc.flags {
				args = append(args, strings.ReplaceAll(strings.ReplaceAll(flag, "{file}", input), "{root}", root))
			}
			if !tc.piped {
				if _, err := io.WriteString(master, strings.ReplaceAll(tc.input, "{file}", input)); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- execute(args, deps) }()
			select {
			case err := <-done:
				if tc.redact && err != nil && strings.Contains(err.Error(), tc.agent) {
					t.Fatal("error echoed credential-shaped name")
				}
				if tc.want != "" {
					if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("error=%v, want %q", err, tc.want)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				master.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("create reader did not stop")
				}
				t.Fatal("create stalled; later confirmation input may have been consumed")
			}
			if pipe != nil {
				remaining, err := io.ReadAll(pipe)
				if err != nil || string(remaining) != tc.input {
					t.Fatalf("nonterminal input consumed: %q %v", remaining, err)
				}
			}
			var transcript strings.Builder
			buf := make([]byte, 8192)
			for {
				ready, err := unix.Poll([]unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}, 0)
				if err != nil {
					t.Fatal(err)
				}
				if ready == 0 {
					break
				}
				n, err := unix.Read(int(master.Fd()), buf)
				if err != nil {
					t.Fatal(err)
				}
				transcript.Write(buf[:n])
			}
			for label, want := range map[string]bool{"Exact model: ": tc.modelPrompt, "Instructions file: ": tc.filePrompt} {
				count := strings.Count(transcript.String(), label)
				if count != 0 && !want || count != 1 && want {
					t.Fatalf("prompt %q count=%d, want=%v: %q", label, count, want, transcript.String())
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil || (len(entries) > 0) != tc.create || *loads != 0 || hidden.Len() != 0 {
				t.Fatalf("unexpected files/config/output: %v %v loads=%d hidden=%q", entries, err, *loads, hidden.String())
			}
			if !tc.create {
				if tc.command == nil && out.Len() != 0 {
					t.Fatalf("refusal reported success: %q", out.String())
				}
				if tc.command != nil && out.Len() == 0 {
					t.Fatal("help/completion produced no output")
				}
				return
			}
			if tc.name == "explicit workspace" && strings.Contains(transcript.String(), "Start one here?") {
				t.Fatal("explicit workspace still asked to initialize")
			}
			member, err := os.ReadFile(filepath.Join(root, "agents/greeter/member.yaml"))
			if err != nil || !strings.Contains(string(member), "model: "+tc.model) {
				t.Fatalf("authored model lost: %s %v", member, err)
			}
			copied, err := os.ReadFile(filepath.Join(root, "agents/greeter/instructions.md"))
			if err != nil || !bytes.Equal(copied, instructions) || !strings.Contains(out.String(), "(not deployed)") {
				t.Fatalf("authored instructions/output changed: %q %v %q", copied, err, out.String())
			}
		})
	}
}
