package app

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

func TestChatTransientClearPreservesDurableResponse(t *testing.T) {
	var out bytes.Buffer
	r := &chatRenderer{out: &out, cursor: true}
	r.beginAssistant("agent")
	r.spinner("agent", "|", time.Second)
	r.spinner("agent", "/", 2*time.Second)
	r.assistant("agent", "first", true)
	before := strings.Count(out.String(), "\033[2K")
	r.assistant("agent", " second\nthird", false)
	r.clearTransient()
	r.assistantOperation("agent", "TOOL CALL", "read", colorBlue, "Status: running")
	r.assistant("agent", "last", true)
	r.finish()
	if before != 2 || strings.Count(out.String(), "\033[2K") != before {
		t.Fatalf("cleared durable text or failed to clear spinner: %q", out.String())
	}
	if !strings.Contains(out.String(), "  | first second\n  | third\n    [TOOL CALL]") {
		t.Fatalf("stream or provenance rails changed: %q", out.String())
	}
}

func TestChatTerminalDetectionRejectsCharacterDevices(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var nilFile *os.File
	for _, writer := range []io.Writer{null, nilFile, &bytes.Buffer{}} {
		if isTerminal(writer) || isInteractiveTerminal(writer) {
			t.Fatalf("nonterminal %T detected as terminal", writer)
		}
	}
}

func TestChatHintAndRowsFitTinyWidths(t *testing.T) {
	for width := 1; width <= 40; width++ {
		for _, input := range []string{slashHint(slashMatches("/")), "e\u0301界👩‍💻abcdefgh"} {
			hint := fitSlashHint(input, width)
			if lipgloss.Width(hint) > max(0, width-2) {
				t.Fatalf("width %d: hint %q", width, hint)
			}
			for _, row := range chatInputRows("  Answer: ", input, width) {
				if lipgloss.Width(row) > width {
					t.Fatalf("width %d: row %q", width, row)
				}
			}
		}
	}
}

func TestChatInvokeFailureRetainsReceivedSession(t *testing.T) {
	for _, ending := range []string{"exit 1", "printf 'broken-json'", ""} {
		t.Run(ending, func(t *testing.T) {
			dir := t.TempDir()
			fakeTool(t, dir, "kagent", `printf '%s\n' '{"contextId":"received-session","taskId":"task","status":{"state":"failed"}}'`+"\n"+ending)
			a := &App{Out: io.Discard, Err: io.Discard}
			view, err := a.invokeStream(context.Background(), dir+"/kagent", "", "agent", "hello", "old-session", "off", &chatRenderer{out: io.Discard}, nil)
			if err == nil || view == nil || view.context != "received-session" {
				t.Fatalf("view=%+v err=%v", view, err)
			}
		})
	}
}

func TestChatHITLFailureRetainsReceivedSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"contextId\":\"received-session\",\"status\":{\"state\":\"failed\"}}\n\ndata: broken\n\n")
	}))
	defer server.Close()
	previous := newStreamView("agent", "off", nil, nil)
	previous.approval = &hitlRequest{TaskID: "task", ContextID: "old", Calls: []hitlCall{{ID: "call", Name: "tool"}}}
	a := &App{Out: io.Discard}
	view, err := a.sendHITL(context.Background(), server.URL, "agent", previous, map[string]any{"decision_type": "reject"}, "off", nil, nil)
	if err == nil || view == nil || view.context != "received-session" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestChatNativePromptTracksTextAndIndent(t *testing.T) {
	var out bytes.Buffer
	r := &chatRenderer{out: &out}
	for _, prompt := range []string{"Answer:", "Approve? [y/N]:", "Rejection reason (optional):"} {
		r.operationPrompt("NATIVE", colorYellow, "payload", prompt)
		if r.promptText != prompt+" " || r.promptIndent != 2 {
			t.Fatalf("prompt state: %+v", r)
		}
	}
	r.prompt()
	if r.promptText != "YOU > " || r.promptIndent != 0 {
		t.Fatalf("chat prompt state: %+v", r)
	}
}

func TestChatFailedStatusKeepsSanitizedReply(t *testing.T) {
	var out bytes.Buffer
	r := &chatRenderer{out: &out, cursor: true}
	view := newStreamView("agent", "off", r, nil)
	view.consume(streamEvent{ContextID: "session", Status: json.RawMessage(`{"state":"failed","message":{"role":"agent","parts":[{"kind":"text","text":"partial\u001b[2J reply"}]}}`)}, &out)
	r.finish()
	if out.String() != "AGENT (agent)\n  | partial reply\n\n" {
		t.Fatalf("failed reply: %q", out.String())
	}
}

func TestChatResumeClearsRetryOnlyAfterValidatedHistory(t *testing.T) {
	// Like the slash registry test, inspect the dispatch itself without a live
	// cluster or replacing the readiness/governance checks with test hooks.
	file, err := parser.ParseFile(token.NewFileSet(), "chat_interactive.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		resume := false
		for _, expression := range clause.List {
			ast.Inspect(expression, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Value == `"/resume "` {
					resume = true
				}
				return true
			})
		}
		if !resume {
			return true
		}
		validated := false
		for _, statement := range clause.Body {
			if check, ok := statement.(*ast.IfStmt); ok {
				ast.Inspect(check.Init, func(node ast.Node) bool {
					if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "showSessionHistory" {
						validated = true
					}
					return true
				})
			}
			if assignment, ok := statement.(*ast.AssignStmt); ok && len(assignment.Lhs) == 2 && len(assignment.Rhs) == 2 {
				left, leftOK := assignment.Lhs[0].(*ast.Ident)
				right, rightOK := assignment.Lhs[1].(*ast.Ident)
				value, valueOK := assignment.Rhs[1].(*ast.BasicLit)
				found = validated && leftOK && rightOK && valueOK && left.Name == "session" && right.Name == "last" && value.Value == `""`
			}
		}
		return false
	})
	if !found {
		t.Fatal("successful /resume must clear retry history after session validation")
	}
}

func TestChatRichSpacingAndExitReasons(t *testing.T) {
	for _, rich := range []bool{false, true} {
		for _, reason := range []string{"end of input", "cancelled", "exit requested"} {
			var out bytes.Buffer
			r := &chatRenderer{out: &out, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: rich, Width: 80})}
			r.prompt()
			r.submitted(false)
			r.submitted(false)
			r.assistant("agent", "answer", true)
			r.assistantOperation("agent", "TOOL CALL", "read", colorBlue, "Status: running")
			r.finish()
			r.exit(reason)
			text := out.String()
			if rich {
				if !strings.HasPrefix(text, "YOU > \n\nAGENT") || !strings.Contains(text, "answer\n\n    [TOOL CALL]") || !strings.HasSuffix(text, "Status: running\n\nChat ended ("+reason+").\n") || strings.Contains(text, "\n\n\n") {
					t.Fatalf("rich spacing/exit: %q", text)
				}
			} else if !strings.HasPrefix(text, "YOU > \nAGENT") || !strings.HasSuffix(text, "[CHAT]\n  Status: ended\n\n") {
				t.Fatalf("plain transcript changed: %q", text)
			}
		}
	}
}

func TestChatWorkingFeedbackLifecycle(t *testing.T) {
	for _, rich := range []bool{false, true} {
		for _, ending := range []string{"completed", "invalid"} {
			var out bytes.Buffer
			r := &chatRenderer{out: &out, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: rich, Width: 80})}
			r.working("Connecting")
			r.working("Checking posture")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				io.WriteString(w, "data: {\"status\":{\"state\":\""+ending+"\"}}\n\n")
			}))
			previous := newStreamView("agent", "off", r, nil)
			previous.approval = &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "one", Name: "tool"}}}
			a := &App{Out: &out}
			_, err := a.sendHITL(context.Background(), server.URL, "agent", previous, map[string]any{"decision_type": "reject"}, "off", r, nil)
			server.Close()
			if (err != nil) != (ending == "invalid") {
				t.Fatalf("stream end: %v", err)
			}
			r.prompt()
			if r.transient || strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "Submitting decision; waiting for agent") != rich || strings.Contains(out.String(), "Connecting") != rich {
				t.Fatalf("working feedback lifecycle: %q", out.String())
			}
		}
	}
}

func TestChatSpinnerPausesAtTerminalAndApprovalEvents(t *testing.T) {
	for _, state := range []string{"completed", "failed", "canceled", "rejected", "input-required", "final", "approval"} {
		t.Run(state, func(t *testing.T) {
			var out bytes.Buffer
			r := &chatRenderer{out: &out, cursor: true}
			view := newStreamView("agent", "summary", r, nil)
			r.assistant("agent", "durable", true)
			view.consumeTool("function_call", false, json.RawMessage(`{"id":"one","name":"read","args":{}}`), &out)
			r.spinner("agent", "|", time.Second)
			event := streamEvent{Status: json.RawMessage(`{"state":"` + state + `"}`)}
			if state == "final" {
				event = streamEvent{Final: true}
			} else if state == "approval" {
				event.Status = json.RawMessage(`{"state":"working","message":{"parts":[{"kind":"data","metadata":{"kagent_type":"function_call","kagent_is_long_running":true},"data":{"name":"adk_request_confirmation","args":{"originalFunctionCall":{"id":"one","name":"read","args":{}}}}}]}}`)
				view.taskID, view.context = "task", "context"
			}
			view.consume(event, &out)
			before := out.String()
			r.spinner("agent", "/", 2*time.Second)
			if r.transient || !r.spinnerPaused || out.String() != before || !strings.Contains(before, "durable") {
				t.Fatalf("terminal event left live spinner: %q", out.String())
			}
			r.operationPrompt("NATIVE APPROVAL", colorYellow, "Tool: read", "Approve? [y/N]:")
			r.submitted(false)
			before = out.String()
			r.spinner("agent", "/", 3*time.Second)
			if out.String() != before {
				t.Fatal("spinner resumed after native prompt")
			}
			r.pauseSpinner(false)
			r.beginAssistant("agent")
			r.spinner("agent", "|", time.Second)
			if !r.transient {
				t.Fatal("new invocation did not resume progress")
			}
			r.assistant("agent", "resumed", true)
			before = out.String()
			r.spinner("agent", "/", 2*time.Second)
			r.clearTransient()
			if out.String() != before {
				t.Fatal("spinner erased response text")
			}
		})
	}
}
