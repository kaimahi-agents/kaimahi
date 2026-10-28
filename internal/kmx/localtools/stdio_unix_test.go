//go:build unix

package localtools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func stdioFixture(t *testing.T, mode string) StdioConfig {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return StdioConfig{Command: program, Args: []string{"-test.run=^TestStdioHelperProcess$", "--", mode}, Dir: t.TempDir(), Env: map[string]string{}, Tools: map[string]string{"ping": "remote_ping"}}
}

func stdioCall(ctx context.Context, host *StdioHost) (api.ToolResult, error) {
	return host.Host().Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", Tool: "ping", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
}

func TestStdioHostMappingEnvironmentAndClose(t *testing.T) {
	t.Setenv("STDIO_AMBIENT_CANARY", "must-not-inherit")
	for _, mode := range []string{"structured", "text", "text-complete", "text-empty", "mixed", "stderr", "empty-env"} {
		t.Run(mode, func(t *testing.T) {
			cfg := stdioFixture(t, mode)
			if mode != "empty-env" {
				cfg.Env["STDIO_FROZEN"] = "frozen-before-start"
				cfg.Env["DEBUG"] = "1"
			}
			host, err := StartStdioHost(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Close() })
			cfg.Env["STDIO_FROZEN"] = "edited-after-start"
			cfg.Tools["ping"] = "not-offered"
			if defs := host.Host().Definitions(); len(defs) != 1 || defs[0].Name != "ping" {
				t.Fatalf("unselected tools offered: %+v", defs)
			}
			result, err := stdioCall(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "text") {
				if result.Output["text"] != "plain answer" {
					t.Fatalf("text projection: %+v", result)
				}
			} else if result.Output["ok"] != true {
				t.Fatalf("env/mapping/structured projection: %+v", result)
			}
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			if err := host.Close(); err != nil {
				t.Fatal("double close changed outcome", err)
			}
			if host.process.cmd.ProcessState == nil {
				t.Fatal("root process was not reaped")
			}
			if _, err := stdioCall(t.Context(), host); err == nil {
				t.Fatal("closed host dispatched again")
			}
		})
	}
}

func TestStdioStartupAndProtocolFailures(t *testing.T) {
	for _, mode := range []string{"exit", "silent", "bad-version", "duplicate", "oversized", "trailing", "echo-init", "escaped-echo-init", "echo-schema", "missing-tool"} {
		t.Run(mode, func(t *testing.T) {
			cfg := stdioFixture(t, mode)
			cfg.Env["STDIO_SECRET"] = "private-stdio-canary"
			cfg.CredentialEnv = []string{"STDIO_SECRET"}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			host, err := StartStdioHost(ctx, cfg)
			if err == nil {
				_ = host.Close()
				t.Fatal("invalid startup accepted")
			}
			if mode == "silent" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("silent startup must retain deadline: %v", err)
				}
			} else if !errors.Is(err, ErrStdio) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("invalid protocol must fail before timeout: %v", err)
			}
			if strings.Contains(err.Error(), "private-stdio-canary") || strings.Contains(err.Error(), cfg.Command) {
				t.Fatal("startup error disclosed private data")
			}
		})
	}
	cfg := stdioFixture(t, "structured")
	cfg.Command = filepath.Join(cfg.Dir, "missing-program")
	if _, err := StartStdioHost(t.Context(), cfg); err == nil || strings.Contains(err.Error(), cfg.Command) {
		t.Fatal("missing program not safely classified", err)
	}
}

func TestStdioRejectsCredentialArguments(t *testing.T) {
	cfg := stdioFixture(t, "structured")
	cfg.Args = append(cfg.Args, "ghp_"+strings.Repeat("a", 36))
	host, err := StartStdioHost(t.Context(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatal("credential-shaped argument reached the child")
	}
}

func TestStdioShortCredentialsStillChecked(t *testing.T) {
	cfg := stdioFixture(t, "structured")
	cfg.Env["STDIO_SECRET"] = "1"
	cfg.CredentialEnv = []string{"STDIO_SECRET"}
	owner, err := StartStdioHost(t.Context(), cfg)
	if err == nil {
		_ = owner.Close()
		t.Fatal("short credential echo in protocol was accepted")
	}
}

func TestStdioCallFailuresNeverSucceed(t *testing.T) {
	for _, mode := range []string{"crash", "echo-result", "escaped-echo-result", "error", "needs-input", "unknown-result", "binary", "oversized-result", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			cfg := stdioFixture(t, mode)
			cfg.Env["STDIO_SECRET"] = "private-stdio-canary"
			cfg.CredentialEnv = []string{"STDIO_SECRET"}
			countFile := filepath.Join(cfg.Dir, "requests")
			cfg.Args = append(cfg.Args, countFile)
			host, err := StartStdioHost(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			if result, err := stdioCall(ctx, host); err == nil || strings.Contains(err.Error(), "private-stdio-canary") {
				t.Fatalf("unsafe call accepted or leaked: %+v %v", result, err)
			}
			err = host.Close()
			// Darwin can deny SIGKILL to a zombie-only group. Do not ignore that
			// real kernel error: uncertain cleanup must still veto success.
			deadGroupDenied := runtime.GOOS == "darwin" && mode == "crash" && host.process.observeErr == nil && errors.Is(host.process.killErr, syscall.EPERM) && host.process.cmd.ProcessState != nil && host.process.cmd.ProcessState.ExitCode() == 7
			if deadGroupDenied {
				if !errors.Is(err, ErrStdioCleanup) {
					t.Fatalf("kernel kill failure must remain cleanup failure: %v", err)
				}
			} else if !errors.Is(err, ErrStdio) || errors.Is(err, ErrStdioCleanup) {
				t.Fatalf("failed call must poison the session without claiming failed cleanup: %v (observe=%v kill=%v state=%v)", err, host.process.observeErr, host.process.killErr, host.process.cmd.ProcessState)
			}
			if data, err := os.ReadFile(countFile); err != nil || string(data) != "call\n" {
				t.Fatalf("tool request retried or never sent: %q %v", data, err)
			}
		})
	}
}

func TestStdioCancellationKillsDescendants(t *testing.T) {
	cfg := stdioFixture(t, "descendant")
	heartbeat := filepath.Join(cfg.Dir, "heartbeat")
	cfg.Args = append(cfg.Args, heartbeat)
	ctx, cancel := context.WithCancel(t.Context())
	host, err := StartStdioHost(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = host.Close() })
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(heartbeat); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := host.Close(); err == nil {
		t.Fatal("cancellation not retained")
	}
	first, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	last, err := os.ReadFile(heartbeat)
	if err != nil || string(first) != string(last) {
		t.Fatal("descendant survived group cleanup", err)
	}
}

func TestStdioResultTypesBeforeDecode(t *testing.T) {
	for _, tc := range []struct {
		field string
		ok    bool
	}{
		{"", true},
		{`,"resultType":""`, true},
		{`,"resultType":"complete"`, true},
		{`,"resultType":"future-state"`, false},
		{`,"resultType":null`, false},
	} {
		raw := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"answer"}]` + tc.field + "}}\n"
		p := &stdioProcess{scanner: bufio.NewScanner(strings.NewReader(raw)), check: func(...string) error { return nil }}
		buf := make([]byte, len(raw))
		n, err := p.Read(buf)
		if tc.ok && (err != nil || string(buf[:n]) != raw) || !tc.ok && (!errors.Is(err, ErrStdio) || n != 0) {
			t.Fatalf("result field %q: forwarded=%q err=%v", tc.field, buf[:n], err)
		}
	}
}

func TestStdioKilledLeaderCannotPassCleanup(t *testing.T) {
	cfg := stdioFixture(t, "descendant")
	heartbeat := filepath.Join(cfg.Dir, "heartbeat")
	cfg.Args = append(cfg.Args, heartbeat)
	host, err := StartStdioHost(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	waitHeartbeat := func(orphan bool) {
		deadline := time.Now().Add(2 * time.Second)
		for {
			data, err := os.ReadFile(heartbeat)
			if err == nil && (!orphan || strings.HasPrefix(string(data), "orphan:")) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("descendant heartbeat missing (orphan=%v): %q %v", orphan, data, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitHeartbeat(false)
	if err := syscall.Kill(host.process.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// Reparenting independently proves leader exit, without Wait/reaping it.
	// The descendant is still writing and holds the protocol stdout open.
	waitHeartbeat(true)
	if err := host.Close(); !errors.Is(err, ErrStdio) {
		t.Fatalf("preexisting leader death accepted as owned cleanup: %v", err)
	}
	if host.process.cmd.ProcessState == nil {
		t.Fatal("root process was not reaped")
	}
	if err := host.Close(); !errors.Is(err, ErrStdio) {
		t.Fatalf("second Close forgot unexpected leader exit: %v", err)
	}
	first, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	last, err := os.ReadFile(heartbeat)
	if err != nil || string(first) != string(last) {
		t.Fatal("descendant survived crash cleanup", err)
	}
}

// A real subprocess fixture, never linked into the installed CLI.
func TestStdioHelperProcess(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || at+1 == len(os.Args) {
		return
	}
	mode := os.Args[at+1]
	if mode == "heartbeat" {
		parent := os.Getppid()
		for {
			value := time.Now().String()
			if os.Getppid() != parent {
				value = "orphan:" + value
			}
			_ = os.WriteFile(os.Args[at+2], []byte(value), 0600)
			time.Sleep(10 * time.Millisecond)
		}
	}
	if mode == "exit" {
		os.Exit(7)
	}
	if mode == "silent" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if slices.Contains([]string{"bad-version", "duplicate", "oversized", "trailing", "echo-init", "escaped-echo-init"}, mode) {
		line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &request)
		if mode == "oversized" {
			fmt.Print(strings.Repeat("x", (1<<20)+1)) // no newline
		} else {
			name, version := "fixture", mcpProtocol
			if mode == "bad-version" {
				version = "1900-01-01"
			}
			if strings.Contains(mode, "echo-init") {
				name = os.Getenv("STDIO_SECRET")
			}
			result := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":%q,"version":"1"}}}`, request.ID, version, name)
			if mode == "duplicate" {
				result = strings.Replace(result, `"result":`, `"result":{},"result":`, 1)
			}
			if mode == "escaped-echo-init" {
				result = strings.Replace(result, "private-stdio-canary", `\u0070rivate-stdio-canary`, 1)
			}
			if mode == "trailing" {
				result += `{}`
			}
			fmt.Println(result)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if mode == "stderr" {
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("private stderr", 100000))
	}
	if mode == "descendant" {
		child := exec.Command(os.Args[0], "-test.run=^TestStdioHelperProcess$", "--", "heartbeat", os.Args[at+2])
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(9)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)})
	name, description := "remote_ping", "Read a value."
	if mode == "missing-tool" {
		name = "other"
	}
	if mode == "echo-schema" {
		description = os.Getenv("STDIO_SECRET")
	}
	server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if at+2 < len(os.Args) && mode != "descendant" {
			file, err := os.OpenFile(os.Args[at+2], os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(9)
			}
			_, _ = file.WriteString("call\n")
			_ = file.Close()
		}
		if mode == "crash" {
			os.Exit(7)
		}
		if mode == "blocked" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		ok := os.Getenv("STDIO_AMBIENT_CANARY") == "" && (mode == "empty-env" && len(os.Environ()) == 0 || mode != "empty-env" && os.Getenv("STDIO_FROZEN") == "frozen-before-start")
		result := &mcp.CallToolResult{StructuredContent: map[string]any{"ok": ok}}
		switch mode {
		case "text":
			result.StructuredContent = nil
			result.Content = []mcp.Content{&mcp.TextContent{Text: "plain answer"}}
		case "text-complete", "text-empty":
			kind := ""
			if mode == "text-complete" {
				kind = "complete"
			}
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"resultType":%q,"content":[{"type":"text","text":"plain answer"}]}`, kind)), result); err != nil {
				os.Exit(9)
			}
		case "mixed":
			result.Content = []mcp.Content{&mcp.TextContent{Text: "extra text"}}
		case "echo-result", "escaped-echo-result":
			value := os.Getenv("STDIO_SECRET")
			if mode == "escaped-echo-result" {
				value = `{"hidden":"\u0070rivate-stdio-canary"}`
			}
			result.StructuredContent = map[string]any{"value": value}
		case "error":
			result.IsError = true
		case "needs-input":
			result.RequestState = "pending"
		case "unknown-result":
			if err := json.Unmarshal([]byte(`{"resultType":"future-state","content":[{"type":"text","text":"not complete"}]}`), result); err != nil {
				os.Exit(9)
			}
		case "binary":
			result.Content = []mcp.Content{&mcp.ImageContent{Data: []byte("not text"), MIMEType: "image/png"}}
		case "oversized-result":
			result.StructuredContent = map[string]any{"text": strings.Repeat("x", 33<<10)}
		}
		return result, nil
	})
	server.AddTool(&mcp.Tool{Name: "unselected", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { os.Exit(8); return nil, nil })
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}
