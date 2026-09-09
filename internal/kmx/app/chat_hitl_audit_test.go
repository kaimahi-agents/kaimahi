package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

func TestChatHITLIncompleteConfirmationsFailClosed(t *testing.T) {
	valid := `{"originalFunctionCall":{"id":"one","name":"tool","args":{}}}`
	for _, parts := range []string{
		`[` + valid + `,{"originalFunctionCall":{"name":"unseen","args":{}}}]`,
		`[` + valid + `,null]`,
		`[` + valid + `,{}]`,
		`[` + valid + `,` + valid + `]`,
		`[{"originalFunctionCall":{"id":"one","args":{}}}]`,
		`[]`, `null`, `{}`, `"invalid"`,
	} {
		t.Run(parts, func(t *testing.T) {
			view := newStreamView("agent", "off", nil, nil)
			view.context, view.taskID = "session", "task"
			raw := json.RawMessage(`{"name":"adk_request_confirmation","args":{"originalFunctionCall":{"id":"fallback","name":"tool"},"toolConfirmation":{"payload":{"hitl_parts":` + parts + `}}}}`)
			view.consumeTool("function_call", true, raw, io.Discard)
			if view.approvalErr == nil {
				t.Fatalf("invalid batch accepted: %+v", view.approval)
			}
			// An invalid wrapper must not disappear when a valid one follows.
			view.consumeTool("function_call", true, json.RawMessage(`{"name":"adk_request_confirmation","args":`+valid+`}`), io.Discard)
			a := &App{Out: io.Discard}
			if _, err := a.sendHITL(context.Background(), ":invalid", "agent", view, map[string]any{"decision_type": "approve"}, "off", nil, nil); err != view.approvalErr {
				t.Fatalf("did not stop at validation: %v", err)
			}
		})
	}
}

func TestChatHITLMalformedAndRepeatedWrappersFailClosed(t *testing.T) {
	valid := json.RawMessage(`{"name":"adk_request_confirmation","args":{"originalFunctionCall":{"id":"one","name":"tool","args":{}}}}`)
	for _, raw := range []json.RawMessage{valid, json.RawMessage(`{"name":"adk_request_confirmation","args":"invalid"}`), json.RawMessage(`{"name":"adk_request_confirmation","id":4}`)} {
		view := newStreamView("agent", "off", nil, nil)
		view.context, view.taskID = "session", "task"
		view.consumeTool("function_call", true, valid, io.Discard)
		view.consumeTool("function_call", true, raw, io.Discard)
		if view.approvalErr == nil {
			t.Fatalf("invalid wrapper accepted: %s", raw)
		}
	}
}

func TestChatHITLValidDecisionProtocol(t *testing.T) {
	for _, tc := range []struct {
		calls       []hitlCall
		input, want string
	}{
		{[]hitlCall{{ID: "one", Name: "tool", Args: json.RawMessage(`{"exact":"args"}`)}}, "y\n", `{"decision_type":"approve"}`},
		{[]hitlCall{{ID: "one", Name: "tool"}}, "n\n\n", `{"decision_type":"reject"}`},
		{[]hitlCall{{ID: "one", Name: "tool"}, {ID: "two", Name: "other"}}, "y\nn\nunsafe\n", `{"decision_type":"batch","decisions":{"one":"approve","two":"reject"},"rejection_reasons":{"two":"unsafe"}}`},
	} {
		var out bytes.Buffer
		a := &App{Out: &out}
		request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: tc.calls}
		decision, err := a.promptHITL(context.Background(), &chatInput{scanner: bufio.NewScanner(strings.NewReader(tc.input))}, request, &chatRenderer{out: &out})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(decision)
		if err != nil || string(encoded) != tc.want {
			t.Fatalf("decision %s, %v; want %s", encoded, err, tc.want)
		}
		if !strings.Contains(out.String(), "Call ID: one") {
			t.Fatalf("exact call not identified: %s", out.String())
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var envelope struct {
				Params struct {
					Message struct {
						Parts []struct {
							Data json.RawMessage `json:"data"`
						} `json:"parts"`
					} `json:"message"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				t.Error(err)
			}
			if len(envelope.Params.Message.Parts) == 0 || string(envelope.Params.Message.Parts[0].Data) != tc.want {
				t.Errorf("wire decision differs: %+v", envelope)
			}
			io.WriteString(w, "data: {\"status\":{\"state\":\"completed\"}}\n\n")
		}))
		previous := newStreamView("agent", "off", nil, nil)
		previous.approval = request
		_, err = a.sendHITL(context.Background(), server.URL, "agent", previous, decision, "off", nil, nil)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestChatHITLRefusesBeforePrompting(t *testing.T) {
	valid := hitlCall{ID: "one", Name: "tool", Args: json.RawMessage(`{}`)}
	oversize := hitlCall{ID: "large-exact-call", Name: "delete", Args: json.RawMessage(`"` + strings.Repeat("x", maxNativeApprovalArgs) + `"`)}
	for _, calls := range [][]hitlCall{nil, {valid, {Name: "missing"}}, {valid, valid}, {valid, oversize}, {{ID: "question", Name: "ask_user"}, valid}} {
		var out bytes.Buffer
		a := &App{Out: &out}
		request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: calls}
		// nil input proves no approval was offered, even for preceding valid calls.
		decision, err := a.promptHITL(context.Background(), nil, request, &chatRenderer{out: &out})
		if err == nil || decision != nil || out.Len() != 0 {
			t.Fatalf("unsafe request prompted: %v %v %q", decision, err, out.String())
		}
		if len(calls) == 2 && calls[1].ID == oversize.ID && !strings.Contains(err.Error(), oversize.ID) {
			t.Fatalf("refusal did not identify exact oversized call: %v", err)
		}
	}
}

func TestChatHITLBatchCannotSendGlobalOrPartialDecision(t *testing.T) {
	a := &App{Out: io.Discard}
	previous := newStreamView("agent", "off", nil, nil)
	previous.approval = &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "one", Name: "tool"}, {ID: "two", Name: "other"}}}
	for _, decision := range []map[string]any{
		{"decision_type": "approve"},
		{"decision_type": "batch", "decisions": map[string]string{"one": "approve"}},
		{"decision_type": "batch", "decisions": map[string]string{"one": "approve", "unseen": "approve"}},
	} {
		if _, err := a.sendHITL(context.Background(), ":invalid", "agent", previous, decision, "off", nil, nil); err == nil || !strings.Contains(err.Error(), "HITL batch") {
			t.Fatalf("batch not refused: %v", err)
		}
	}
}

func TestChatHITLInspectionLimitShowsAllArguments(t *testing.T) {
	args := json.RawMessage(`"` + strings.Repeat("x", maxNativeApprovalArgs-2) + `"`)
	var out bytes.Buffer
	a := &App{Out: &out}
	request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "exact", Name: "tool", Args: args}}}
	decision, err := a.promptHITL(context.Background(), &chatInput{scanner: bufio.NewScanner(strings.NewReader("y\n"))}, request, &chatRenderer{out: &out})
	if err != nil || decision["decision_type"] != "approve" || !strings.Contains(out.String(), string(args)) || strings.Contains(out.String(), "truncated") {
		t.Fatalf("inspectable boundary call failed: %v; output length %d", err, out.Len())
	}
	request.Calls[0].Args = append(args, ' ')
	out.Reset()
	if _, err := a.promptHITL(context.Background(), nil, request, &chatRenderer{out: &out}); err == nil || out.Len() != 0 {
		t.Fatal("oversized exact call offered approval")
	}
}

func TestChatQuestionsPreserveAnswerSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, question, input string
		want                  []string
		invalid               bool
	}{
		{"free text", `{"question":"Where?"}`, " Wellington, New Zealand \n", []string{" Wellington, New Zealand "}, false},
		{"multiple free text", `{"question":"Where?","multiple":true}`, "Wellington, New Zealand\n", []string{"Wellington, New Zealand"}, false},
		{"single comma", `{"question":"Where?","choices":["Wellington, NZ","London"],"multiple":false}`, "Wellington, NZ\n", []string{"Wellington, NZ"}, false},
		{"single rejects multiple", `{"question":"Pick","choices":["a","b"]}`, "a,b\na\n", []string{"a"}, true},
		{"multiple", `{"question":"Pick","choices":["a","b"],"multiple":true}`, "a, b\n", []string{"a", "b"}, false},
		{"multiple comma choice", `{"question":"Pick","choices":["a,b","c"],"multiple":true}`, "\"a,b\", c\n", []string{"a,b", "c"}, false},
		{"empty", `{"question":"Pick"}`, "  \nanswer\n", []string{"answer"}, true},
		{"unknown", `{"question":"Pick","choices":["a","b"],"multiple":true}`, "unknown\na\n", []string{"a"}, true},
		{"duplicate", `{"question":"Pick","choices":["a","b"],"multiple":true}`, "a,a\na\n", []string{"a"}, true},
		{"empty selection", `{"question":"Pick","choices":["a","b"],"multiple":true}`, "a,\na\n", []string{"a"}, true},
		{"bad csv", `{"question":"Pick","choices":["a","b"],"multiple":true}`, "\"a\na\n", []string{"a"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := &App{Out: &out}
			request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "question", Name: "ask_user", Args: json.RawMessage(`{"questions":[` + tc.question + `]}`)}}}
			decision, err := a.promptHITL(context.Background(), &chatInput{scanner: bufio.NewScanner(strings.NewReader(tc.input))}, request, &chatRenderer{out: &out})
			if err != nil {
				t.Fatal(err)
			}
			answers := decision["ask_user_answers"].([]map[string][]string)
			if decision["decision_type"] != "approve" || len(answers) != 1 || !reflect.DeepEqual(answers[0]["answer"], tc.want) {
				t.Fatalf("answer changed: %+v", decision)
			}
			if strings.Contains(out.String(), "Invalid answer") != tc.invalid {
				t.Fatalf("validation output: %s", out.String())
			}
		})
	}
}

func TestChatQuestionsRejectMalformedChoicesAndEOF(t *testing.T) {
	for _, question := range []string{`{"question":""}`, `{"question":"Pick","choices":[""]}`, `{"question":"Pick","choices":["a"," a "]}`, `{"question":"Pick","choices":["a\u001b[2J"]}`, `{"question":"Pick","choices":["a\nb"]}`} {
		a := &App{Out: io.Discard}
		request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "question", Name: "ask_user", Args: json.RawMessage(`{"questions":[` + question + `]}`)}}}
		if decision, err := a.promptHITL(context.Background(), nil, request, &chatRenderer{out: io.Discard}); err == nil || decision != nil {
			t.Fatalf("bad question accepted: %s", question)
		}
	}
	a := &App{Out: io.Discard}
	request := &hitlRequest{TaskID: "task", ContextID: "session", Calls: []hitlCall{{ID: "question", Name: "ask_user", Args: json.RawMessage(`{"questions":[{"question":"Pick","choices":["a"]}]}`)}}}
	if decision, err := a.promptHITL(context.Background(), &chatInput{scanner: bufio.NewScanner(strings.NewReader("bad\n"))}, request, &chatRenderer{out: io.Discard}); err != io.EOF || decision != nil {
		t.Fatalf("invalid answer + EOF approved: %v %v", decision, err)
	}
}

func TestChatHistoryClosesFinalNonModelActor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session": map[string]string{"agent_id": "kagent__NS__agent"}, "events": []map[string]string{{"data": `{"author":"agent","content":{"role":"agent","parts":[{"text":"last response"}]}}`}}}})
	}))
	defer server.Close()
	var out bytes.Buffer
	a := &App{Out: &out}
	if err := a.showSessionHistory(server.URL, "session", "agent", "off", newChatRenderer(&out)); err != nil {
		t.Fatal(err)
	}
	newChatRenderer(&out).prompt()
	if !strings.HasSuffix(out.String(), "  | last response\n\nYOU > ") {
		t.Fatalf("history left an active actor: %q", out.String())
	}
}

func TestChatStaticNativeCallouts(t *testing.T) {
	for _, width := range []int{20, 100} {
		for _, color := range []bool{false, true} {
			for _, question := range []bool{false, true} {
				var out bytes.Buffer
				r := &chatRenderer{out: &out, color: color, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: color, Width: width})}
				call := hitlCall{ID: "exact\x1b[2J", Name: "tool\x1b]52;c;secret\a", Args: json.RawMessage(`{"value":"` + strings.Repeat("x", maxNativeApprovalArgs-30) + `"}`)}
				input := "y\n"
				if question {
					call.Name, call.Args = "ask_user", json.RawMessage(`{"questions":[{"question":"Pick\u001b[2J","choices":["one","two"]}]}`)
					input = "one\n"
				}
				a := &App{Out: &out}
				request := &hitlRequest{TaskID: "task", ContextID: "session", Hint: "inspect\x1b[2J", Calls: []hitlCall{call}}
				if _, err := a.promptHITL(context.Background(), &chatInput{scanner: bufio.NewScanner(strings.NewReader(input))}, request, r); err != nil {
					t.Fatal(err)
				}
				text := ansi.Strip(out.String())
				bottom := strings.LastIndex(text, "╰")
				label, prompt := "[NATIVE APPROVAL]", "Approve? [y/N]:"
				if question {
					label, prompt = "[NATIVE QUESTION]", "Answer:"
				}
				if bottom < 0 || !strings.Contains(text[bottom:], "\n"+label+"\n  "+prompt) || strings.Contains(out.String(), "\x1b[2J") || strings.Contains(out.String(), "secret") {
					t.Fatalf("unsafe/detached prompt: %q", text)
				}
				if !question && strings.Count(text, "x") != strings.Count(string(call.Args), "x")+1 {
					t.Fatalf("arguments lost in callout at width %d", width)
				}
				if !color && strings.Contains(out.String(), "\x1b") {
					t.Fatal("no-color callout emitted escapes")
				}
				start := strings.Index(text, "╭")
				for _, line := range strings.Split(text[start:bottom], "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("callout width %d overflow: %q", width, line)
					}
				}
			}
		}
	}
}
