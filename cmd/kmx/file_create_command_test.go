package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"go.yaml.in/yaml/v3"
)

// An interrupted command must not create even its requested directories.
func TestFileCreateCancelled(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := createAgentFiles(ctx, "nested", "greeter", "Greeting", writeNewAgentFile)
	if err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, statErr := os.Stat("nested"); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled create had effects: %v", statErr)
	}
}

func TestFileCreateRaceAndPartialFailure(t *testing.T) {
	for _, failure := range []string{"collision", "write"} {
		t.Run(failure, func(t *testing.T) {
			t.Chdir(t.TempDir())
			calls := 0
			write := func(path, content string) (bool, error) {
				calls++
				if calls == 2 {
					if err := os.WriteFile(path, []byte("keep me"), 0644); err != nil {
						t.Fatal(err)
					}
					if failure == "write" {
						return true, io.ErrShortWrite
					}
				}
				return writeNewAgentFile(path, content)
			}
			_, _, err := createAgentFiles(t.Context(), ".", "greeter", "Greeting", write)
			if err == nil || !strings.Contains(err.Error(), "created files retained") || !strings.Contains(err.Error(), "greeter.agent.md") || strings.Contains(err.Error(), "\n") {
				t.Fatalf("partial effect missing: %v", err)
			}
			got, readErr := os.ReadFile("greeter.agent.test.md")
			if readErr != nil || string(got) != "keep me" {
				t.Fatalf("changed winner/partial file: %q, %v", got, readErr)
			}
			if failure == "write" && !strings.Contains(err.Error(), "greeter.agent.test.md") {
				t.Fatalf("missing second partial: %v", err)
			}
			if _, err := os.Stat("greeter.agent.md"); err != nil {
				t.Fatal("deleted acknowledged first file")
			}
		})
	}
}

type faultAgentCreationFile struct {
	writeErr, syncErr, closeErr error
	short, closed               bool
}

func (f *faultAgentCreationFile) WriteString(s string) (int, error) {
	if f.short {
		return 0, f.writeErr
	}
	return len(s), f.writeErr
}
func (f *faultAgentCreationFile) Sync() error  { return f.syncErr }
func (f *faultAgentCreationFile) Close() error { f.closed = true; return f.closeErr }

func TestFileCreateWriteFaults(t *testing.T) {
	fault := errors.New("disk fault")
	for _, file := range []faultAgentCreationFile{
		{writeErr: fault}, {short: true}, {syncErr: fault}, {closeErr: fault},
	} {
		err := finishAgentFile(&file, "complete bytes")
		if err == nil || !file.closed {
			t.Fatalf("lost write/close error: %v / closed %v", err, file.closed)
		}
	}
	t.Chdir(t.TempDir())
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stdout = previewBrokenWriter{}
	err := execute([]string{"create", "greeter"}, deps)
	if err == nil || exitCode(err) != 2 {
		t.Fatalf("lost stdout error: %v", err)
	}
	for _, path := range []string{"greeter.agent.md", "greeter.agent.test.md"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("output failure deleted created file")
		}
	}
}

func TestFileCreatePair(t *testing.T) {
	for _, destination := range []string{"cwd", "nested", "repository", "repository cwd"} {
		t.Run(destination, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			// An existing .github folder must not change the default destination.
			if err := os.MkdirAll(filepath.Join(root, ".github", "agents"), 0755); err != nil {
				t.Fatal(err)
			}
			args := []string{"create", "greeter"}
			agentPath, testsPath := "greeter.agent.md", "greeter.agent.test.md"
			switch destination {
			case "nested":
				args = append(args, "--path", "new folder/agents")
				agentPath, testsPath = "new folder/agents/greeter.agent.md", "new folder/agents/greeter.agent.test.md"
			case "repository":
				args = append(args, "--path", ".github/agents")
				agentPath, testsPath = ".github/agents/greeter.agent.md", ".github/agent-tests/greeter.agent.test.md"
			case "repository cwd":
				t.Chdir(filepath.Join(root, ".github", "agents"))
				testsPath = "../agent-tests/greeter.agent.test.md"
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 || diag.Len() != 0 {
				t.Fatalf("offline create loaded config or prompted: %d / %s", *loads, &diag)
			}
			wantAgent := "---\nname: \"greeter\"\ndescription: \"A friendly greeting agent\"\n---\n\nGreet the user by name.\nReply with a short, friendly greeting.\n"
			got, err := os.ReadFile(agentPath)
			if err != nil || string(got) != wantAgent {
				t.Fatalf("agent = %q, %v", got, err)
			}
			parsed, err := agentfile.Load(agentPath)
			if err != nil || parsed.Title != "greeter" || parsed.Model != "" {
				t.Fatalf("generated GitHub agent = %+v, %v", parsed, err)
			}
			wantTests := "---\nschemaVersion: 1\n---\n\n# greeter tests\n\n```yaml agent-test\nid: greeting\ninput: Ada\nassertions:\n  - id: greeting-text\n    type: contains\n    value: Ada\n```\n"
			got, err = os.ReadFile(testsPath)
			if err != nil || string(got) != wantTests {
				t.Fatalf("tests = %q, %v", got, err)
			}
			output := out.String()
			if !strings.Contains(output, "Wrote ") || !strings.Contains(output, "Next:\n") || !strings.Contains(output, "kmx run ") || !strings.Contains(output, ` "hi"`) || !strings.Contains(output, "kmx test ") {
				t.Fatalf("missing next steps: %s", output)
			}
			if destination == "cwd" {
				entries, err := os.ReadDir(".github/agents")
				if err != nil || len(entries) != 0 {
					t.Fatalf("default touched .github: %v / %v", entries, err)
				}
			}
		})
	}
}

func TestFileCreateDescriptionAndQuotedNextCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.executable = "/bin/my kmx"
	description := `Replies with "hello": # friendly`
	if err := execute([]string{"create", "-greeter", "--path", "a'b folder", "--description", description, "--"}, deps); err == nil {
		t.Fatal("leading dash must follow --")
	}
	out.Reset()
	if err := execute([]string{"create", "--path", "a'b folder", "--description", description, "--", "-greeter"}, deps); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("a'b folder/-greeter.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	var front struct{ Name, Description string }
	if err := yaml.Unmarshal(bytes.Split(raw, []byte("---"))[1], &front); err != nil || front.Name != "-greeter" || front.Description != description {
		t.Fatalf("frontmatter = %+v / %v", front, err)
	}
	want := "  '/bin/my kmx' run 'a'\"'\"'b folder/-greeter.agent.md' \"hi\"\n  '/bin/my kmx' test 'a'\"'\"'b folder/-greeter.agent.md'\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("unsafe next commands: %s", &out)
	}
	out.Reset()
	if err := execute([]string{"create", "--", "-local"}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "run ./-local.agent.md") || !strings.Contains(out.String(), "test ./-local.agent.md") {
		t.Fatalf("leading dash interpreted as flag: %s", &out)
	}
}

func TestFileCreateRefusesOccupiedPair(t *testing.T) {
	for _, target := range []string{"greeter.agent.md", "greeter.agent.test.md"} {
		for _, kind := range []string{"file", "directory", "symlink"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				t.Chdir(t.TempDir())
				var err error
				switch kind {
				case "file":
					err = os.WriteFile(target, []byte("keep me"), 0644)
				case "directory":
					err = os.Mkdir(target, 0755)
				case "symlink":
					err = os.Symlink("missing", target)
				}
				if err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				err = execute([]string{"create", "greeter"}, deps)
				if err == nil || exitCode(err) != 2 || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "already exists") || out.Len() != 0 || diag.Len() != 0 || *loads != 0 {
					t.Fatalf("collision = %v / exit %d / %s / %s", err, exitCode(err), &out, &diag)
				}
				if kind == "file" {
					got, _ := os.ReadFile(target)
					if string(got) != "keep me" {
						t.Fatalf("overwrote: %q", got)
					}
				}
				entries, _ := os.ReadDir(".")
				if len(entries) != 1 {
					t.Fatalf("partial create on preflight refusal: %v", entries)
				}
			})
		}
	}
}

func TestFileCreateRefusesUnusableSourceWithoutEcho(t *testing.T) {
	credentialShape := "ghp_" + strings.Repeat("a", 36)
	for _, field := range []string{"name", "description"} {
		t.Run(field, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			name, description := "greeter", "Greeting"
			if field == "name" {
				name = credentialShape
			} else {
				description = credentialShape
			}
			err := execute([]string{"create", name, "--description", description, "--path", "new-folder"}, deps)
			if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "invalid name or description") || strings.Contains(err.Error(), credentialShape) || out.Len() != 0 || diag.Len() != 0 || *loads != 0 {
				t.Fatalf("unusable source was written or echoed (field %s)", field)
			}
			entries, _ := os.ReadDir(".")
			if len(entries) != 0 {
				t.Fatal("unusable source created a directory or file")
			}
		})
	}
}

func TestFileCreateRefusesOperationalSelectors(t *testing.T) {
	for _, flag := range []string{"--context", "--container-engine"} {
		t.Run(flag, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute([]string{"create", "greeter", flag, "unused"}, deps)
			if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "refuses "+flag) || out.Len() != 0 || diag.Len() != 0 || *loads != 0 {
				t.Fatalf("operational selector accepted or wrong refusal: %v", err)
			}
			entries, _ := os.ReadDir(".")
			if len(entries) != 0 {
				t.Fatalf("operational selector wrote files: %v", entries)
			}
		})
	}
}

func TestFileCreateInvalidInputs(t *testing.T) {
	for _, args := range [][]string{
		{}, {"a", "b"}, {"../outside"}, {"a/b"}, {`a\b`}, {"."}, {".."}, {""}, {"a\nb"},
		{"greeter", "--path", ""}, {"greeter", "--path", "folder\x1b]0;title\a"}, {"greeter", "--description", "line\nbreak"},
		{"greeter", "--workspace", "."}, {"greeter", "--model", "fake"}, {"greeter", "--instructions", "prompt.md"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(append([]string{"create"}, args...), deps)
			if err == nil || exitCode(err) != 2 || out.Len() != 0 || diag.Len() != 0 || *loads != 0 {
				t.Fatalf("refusal = %v / exit %d / %s / %s", err, exitCode(err), &out, &diag)
			}
			entries, _ := os.ReadDir(".")
			if len(entries) != 0 {
				t.Fatalf("invalid input wrote files: %v", entries)
			}
		})
	}
}

func TestFileCreateHelpAndWindowsGate(t *testing.T) {
	for _, topic := range [][]string{{"create"}, {"package", "create"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(append(append([]string{}, topic...), "--help"), deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || diag.Len() != 0 || !strings.Contains(out.String(), "kmx "+strings.Join(topic, " ")) {
			t.Fatalf("help effects: %s / %s", &out, &diag)
		}
		if len(topic) == 1 && (!strings.Contains(out.String(), "--path") || strings.Contains(out.String(), "--runtime")) {
			t.Fatalf("wrong root Create help: %s", &out)
		}
		deps.goos = "windows"
		out.Reset()
		err := execute(append(append([]string{}, topic...), "greeter"), deps)
		if err == nil || exitCode(err) != 1 || *loads != 0 || out.Len() != 0 {
			t.Fatalf("Windows Create admitted: %v", err)
		}
	}
}
