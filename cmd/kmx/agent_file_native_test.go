package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func TestNativeCLIRefusalsBeforeRunner(t *testing.T) {
	for _, args := range [][]string{
		{"test", "missing.agent.md", "--runner", "copilotd"},
		{"test", "missing.agent.md", "--host", "ws://127.0.0.1:8080"},
		{"test", "--runner", "copilotd", "--host", "ws://127.0.0.1:8080"},
		{"test", "missing.agent.md", "--runner", "copilotd", "--host", "ws://localhost:8080"},
		{"test", "missing.agent.md", "--runner", "copilotd", "--host", "ws://127.0.0.1:8080", "--mcp-server", "saved"},
		{"test", "missing.agent.md", "--runner", "copilotd", "--host", "ws://127.0.0.1:8080", "--no-mcp-servers"},
		{"test", "missing.agent.md", "--runner", "copilotd", "--host", "ws://127.0.0.1:8080", "--tool-bindings", "bindings.json"},
	} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		runner := &cliPreviewFake{}
		deps.fileRunner = runner
		err := execute(args, deps)
		if err == nil || runner.calls != 0 || strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("args=%v err=%v sends=%d", args, err, runner.calls)
		}
	}
}
func TestNativeCLIWindowsGate(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.goos = "windows"
	runner := &cliPreviewFake{}
	deps.fileRunner = runner
	if err := execute([]string{"test", "missing.agent.md", "--runner", "copilotd", "--host", "ws://127.0.0.1:8080"}, deps); !errors.Is(err, errWindowsOperationUnsupported) || runner.calls != 0 {
		t.Fatalf("gate=%v calls=%d", err, runner.calls)
	}
}
func TestNativeSessionLineCheckedQuotedAndFallible(t *testing.T) {
	var out bytes.Buffer
	callback := nativeSessionCreated(&out)
	if err := callback("case", "ahp-session:/abc"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Native session for case \"case\": \"ahp-session:/abc\"\n" {
		t.Fatal(out.String())
	}
	if err := callback("unsafe\ncase", "ahp-session:/abc"); err == nil {
		t.Fatal("multiline accepted")
	}
	if err := nativeSessionCreated(nativeFailWriter{})("case", "ahp-session:/abc"); err == nil {
		t.Fatal("failed output ignored")
	}
}

type nativeFailWriter struct{}

func (nativeFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestNativeSignInAdviceAndTimeoutExit(t *testing.T) {
	for _, code := range []string{"copilot_auth_required", "copilot_keychain_unavailable"} {
		r := app.FilePreviewResult{Runner: "copilotd native execution (no journal; not a receipt)", ReasonCode: code}
		err := fileCommandResultError(r, errors.New("PRIVATE"))
		if !strings.Contains(err.Error(), "gh auth login --hostname github.com") || strings.Contains(err.Error(), "ollama") {
			t.Fatal(err)
		}
	}
}
