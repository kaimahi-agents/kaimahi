//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
)

func cliMCPFixture(t *testing.T, mode, state string, recorded bool) (string, string) {
	t.Helper()
	file := cliPreviewFile(t)
	mock := ""
	if recorded {
		mock = "    mocks:\n      tools:\n        - name: ping\n          mode: recorded\n          calls: []\n"
	}
	source := "# Recorder\n## Identity\n- **ID:** recorder\n- **Purpose:** Record.\n## Accountable Responsibilities\nCall the tool `ping`. Reply with exactly `done`.\n## Non-Responsibilities\nNo effects.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Tools\n- ping\n## Examples\n```yaml\nexamples:\n  - id: sample\n    input: hello\n" + mock + "    assertions:\n      - id: calls\n        type: toolCalled\n        tool: ping\n```\n"
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Runtime outputs must not change the checked source directory identity.
	marker := filepath.Join(t.TempDir(), "started")
	if mode == "missing" {
		program = filepath.Join(filepath.Dir(file), "missing-server")
	}
	doc := map[string]any{"tools": map[string]string{"ping": state}, "mcpServers": map[string]any{"notes": map[string]any{"command": program, "args": []string{"-test.run=^TestCLIMCPHelperProcess$", "--", mode, marker}, "tools": map[string]string{"ping": "remote_ping"}}}, "liveToolServers": []string{"notes"}}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", privateCommandHome(t))
	return file, marker
}

func TestFileCLIMCPSelectionAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, mode, state string
		flags             []string
		exit              int
		live              bool
	}{
		{"saved", "serve", "Enabled", nil, 0, true},
		{"explicit replacement", "serve", "Enabled", []string{"--mcp-server", "missing", "--mcp-server", "notes"}, 2, false},
		{"explicit", "serve", "Enabled", []string{"--mcp-server", "notes"}, 0, true},
		{"clear wins", "serve", "Enabled", []string{"--mcp-server", "missing", "--no-mcp-servers"}, 0, false},
		{"disabled", "serve", "Disabled", nil, 1, false},
		{"startup crash", "exit", "Enabled", nil, 2, true},
		{"missing executable", "missing", "Enabled", nil, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, marker := cliMCPFixture(t, tc.mode, tc.state, true)
			if tc.name == "explicit" {
				path := filepath.Join(filepath.Dir(file), "kmx.yaml")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				doc["liveToolServers"] = []string{"missing"}
				raw, err = yaml.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.fileRunner = productionDependencies().fileRunner
			args := append([]string{"test", file, "-o", "json"}, tc.flags...)
			err := execute(args, deps)
			exit := 0
			if err != nil {
				var e *fileCommandError
				if !errors.As(err, &e) {
					t.Fatal(err)
				}
				exit = e.exit
			}
			if exit != tc.exit || *loads != 0 {
				t.Fatalf("exit %d want%d: %v\n%s\n%s", exit, tc.exit, err, out.String(), diag.String())
			}
			_, started := os.Stat(marker)
			if (started == nil) != tc.live {
				t.Fatalf("unexpected launch: %v", started)
			}
			var result app.FilePreviewResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !tc.live && tc.exit == 0 && (result.Examples[0].Tools[0].Source != "unavailable" || result.SavedRevision != "") {
				t.Fatalf("cleared source changed: %+v", result)
			}
			if tc.live && tc.exit == 0 {
				if result.Examples[0].Tools[0].Source != "live" || result.SavedRevision == "" || !result.FullPass {
					t.Fatalf("capture: %+v", result)
				}
				_ = os.Remove(marker)
				// Launch semantics and credential lookup must be irrelevant to replay.
				bad := []byte("tools: {ping: Enabled}\nmcpServers: false\nliveToolServers: [missing]\n")
				if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), bad, 0600); err != nil {
					t.Fatal(err)
				}
				out.Reset()
				diag.Reset()
				if err := execute([]string{"test", file, "-o", "json"}, deps); err != nil {
					t.Fatal("offline replay", err, out.String())
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Examples[0].Tools[0].Source != "recorded" || !result.FullPass {
					t.Fatalf("replay: %+v %v", result, err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("replay launched")
				}
			}
		})
	}
}

func TestFileCLIMCPCompanionCaptureAndReplay(t *testing.T) {
	file, marker := cliMCPFixture(t, "serve", "Enabled", true)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	agent := []byte(strings.SplitN(string(raw), "## Examples", 2)[0])
	if err := os.WriteFile(file, agent, 0600); err != nil {
		t.Fatal(err)
	}
	tests := strings.TrimSuffix(file, ".agent.md") + ".agent.test.md"
	if err := os.WriteFile(tests, []byte("---\nschemaVersion: 1\n---\n```yaml agent-test\nid: sample\ninput: hello\nmocks: {tools: [{name: ping, mode: recorded, calls: []}]}\nassertions: [{id: calls, type: toolCalled, tool: ping}]\n```\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.fileRunner = productionDependencies().fileRunner
	for _, source := range []string{"live", "recorded"} {
		out.Reset()
		if err := execute([]string{"test", file, "-o", "json"}, deps); err != nil {
			t.Fatal(err, out.String(), diag.String())
		}
		var result app.FilePreviewResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.FullPass || result.Examples[0].Tools[0].Source != source {
			t.Fatalf("%s result: %+v %v", source, result, err)
		}
		if source == "live" {
			if result.RecordingTarget == nil || result.RecordingTarget.Role != "tests" || result.SavedRevision == "" {
				t.Fatalf("missing tests capture: %+v", result)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal("server did not start", err)
			}
		} else if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("replay launched server", err)
		}
		if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, agent) {
			t.Fatal("companion capture changed agent", err)
		}
	}
}

func TestFileCLIMCPRunAndSummary(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		t.Run(op, func(t *testing.T) {
			file, marker := cliMCPFixture(t, "serve", "Enabled", false)
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			deps.fileRunner = productionDependencies().fileRunner
			args := []string{op, file}
			summary := filepath.Join(t.TempDir(), "summary.md")
			if op == "test" {
				t.Setenv("GITHUB_STEP_SUMMARY", summary)
				args = append(args, "-o", "json", "--github-summary")
			}
			if err := execute(args, deps); err != nil {
				t.Fatal(err, out.String(), diag.String())
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("live source not launched", err)
			}
			if op == "run" {
				if out.String() != "done\n" {
					t.Fatalf("Run stdout not answer-only: %q", out.String())
				}
			} else {
				var result app.FilePreviewResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.FullPass || result.Examples[0].Tools[0].Source != "live" {
					t.Fatalf("JSON: %+v %v", result, err)
				}
				if raw, err := os.ReadFile(summary); err != nil || !bytes.Contains(raw, []byte("live")) {
					t.Fatalf("summary: %s %v", raw, err)
				}
			}
		})
	}
}

type failedMCPClose struct{ *localtools.StdioInvocation }

func (o *failedMCPClose) Close() error {
	_ = o.StdioInvocation.Close()
	return localtools.ErrStdioCleanup
}

func TestFileCLIMCPCleanupVeto(t *testing.T) {
	file, _ := cliMCPFixture(t, "serve", "Enabled", true)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := newFileMCPRunner(app.NewLocalFileRunner()).(*fileMCPRunner)
	runner.owner = func(ctx context.Context, file string, selection localtools.ServerSelection) fileMCPOwner {
		return &failedMCPClose{localtools.NewStdioInvocation(ctx, file, selection)}
	}
	deps.fileRunner = runner
	err = execute([]string{"test", file, "-o", "json"}, deps)
	var commandErr *fileCommandError
	if !errors.As(err, &commandErr) || commandErr.exit != 2 {
		t.Fatalf("cleanup exit: %v", err)
	}
	var result app.FilePreviewResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.FullPass || result.State == "complete" || result.SavedRevision != "" {
		t.Fatalf("cleanup accepted completion: %+v %v", result, err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("capture saved before cleanup", err)
	}
}

func TestFileCLIMCPCancelStartup(t *testing.T) {
	file, marker := cliMCPFixture(t, "silent", "Enabled", true)
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.fileRunner = productionDependencies().fileRunner
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	root := newRootCommand(&commandState{deps: deps, argv: []string{"test", file, "-o", "json"}})
	root.SetArgs([]string{"test", file, "-o", "json"})
	err := root.ExecuteContext(ctx)
	<-done
	var commandErr *fileCommandError
	if !errors.As(err, &commandErr) || commandErr.exit != 130 {
		t.Fatalf("startup cancellation masked: %v\n%s", err, out.String())
	}
}

func TestCLIMCPHelperProcess(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 {
		return
	}
	mode := os.Args[at+1]
	if err := os.WriteFile(os.Args[at+2], []byte("started"), 0600); err != nil {
		os.Exit(9)
	}
	if mode == "exit" {
		os.Exit(7)
	}
	if mode == "silent" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)})
	server.AddTool(&mcp.Tool{Name: "remote_ping", Description: "Ping.", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
	})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}
