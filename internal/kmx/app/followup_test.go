package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func fakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveStreamShowsToolsAndFinalReply(t *testing.T) {
	var out bytes.Buffer
	view := &streamView{agent: "hello-tools", toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "summary"}
	input := strings.NewReader(strings.Join([]string{
		`{"kind":"status-update","contextId":"session-1","taskId":"task-1","status":{"state":"working","message":{"role":"agent","parts":[{"kind":"data","metadata":{"kagent_type":"function_call"},"data":{"id":"call-1","name":"get_pods","args":{"namespace":"default"}}}]}}}`,
		`{"kind":"status-update","contextId":"session-1","taskId":"task-1","status":{"state":"working","message":{"role":"agent","parts":[{"kind":"data","metadata":{"kagent_type":"function_response"},"data":{"id":"call-1","name":"get_pods","response":{"isError":false}}}]}}}`,
		`{"kind":"artifact-update","contextId":"session-1","taskId":"task-1","artifact":{"parts":[{"kind":"text","text":"pod-a"}]}}`,
		`{"kind":"status-update","contextId":"session-1","taskId":"task-1","final":true,"status":{"state":"completed"}}`,
	}, "\n"))
	a := &App{Out: &out}
	if err := a.consumeStream(input, view); err != nil {
		t.Fatal(err)
	}
	if view.state != "completed" || view.context != "session-1" || view.reply != "pod-a" {
		t.Fatalf("unexpected view: %+v", view)
	}
	for _, want := range []string{"Tool: get_pods", "completed", "hello-tools: pod-a"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestToolDisplayModesStillDetectGovernanceDenials(t *testing.T) {
	raw := json.RawMessage(`{"id":"call-1","name":"post","response":{"isError":true,"content":[{"text":"not permitted; approval request filed"}]}}`)
	for _, mode := range []string{"off", "summary", "verbose"} {
		var out bytes.Buffer
		view := &streamView{toolCalls: map[string]string{"call-1": "post"}, messageText: map[string]string{}, toolMode: mode, governedTools: map[string]bool{"post": true}}
		view.consumeTool("function_response", false, raw, &out)
		if !view.denied {
			t.Errorf("mode %s hid governance denial", mode)
		}
		if mode == "off" && out.Len() != 0 {
			t.Errorf("off mode printed tool output: %s", out.String())
		}
		if mode == "verbose" && !strings.Contains(out.String(), "result:") {
			t.Errorf("verbose mode omitted result: %s", out.String())
		}
	}
}

func TestGovernedToolRouteIsVisibleWhenToolDisplayIsOff(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := &streamView{
		agent:         "hello-tools",
		toolCalls:     map[string]string{},
		messageText:   map[string]string{},
		toolMode:      "off",
		renderer:      renderer,
		governedTools: map[string]bool{"get_pods": true},
	}
	view.consumeTool("function_call", false, json.RawMessage(`{"id":"call-1","name":"get_pods","args":{}}`), &out)
	view.consumeTool("function_response", false, json.RawMessage(`{"id":"call-1","name":"get_pods","response":{"isError":false,"content":[{"text":"ok"}]}}`), &out)

	want := "AGENT (hello-tools)\n" +
		"    [KAIMAHI ROUTE]\n" +
		"      Seam: MCP gateway\n" +
		"      Tool: get_pods\n" +
		"      Configuration: verified through ready plane at chat start\n" +
		"      Per-call decision: not exposed by kagent stream\n\n"
	if out.String() != want {
		t.Fatalf("governance check was hidden with tool display off:\n%q", out.String())
	}
}

func TestDirectToolDoesNotClaimGovernanceCheck(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := &streamView{toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "off", renderer: renderer, governedTools: map[string]bool{}}
	view.consumeTool("function_call", false, json.RawMessage(`{"id":"call-1","name":"get_pods","args":{}}`), &out)
	view.consumeTool("function_response", false, json.RawMessage(`{"id":"call-1","response":{"isError":true,"content":[{"text":"tool not permitted"}]}}`), &out)
	if out.Len() != 0 || view.denied {
		t.Fatalf("direct tool was presented as governed: output=%q denied=%v", out.String(), view.denied)
	}
}

func TestGovernedDenialSuppressesUnverifiedSuccessReceipt(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "off", renderer: renderer, governedTools: map[string]bool{"post": true}}
	view.consumeTool("function_call", false, json.RawMessage(`{"id":"call-1","name":"post","args":{}}`), &out)
	view.consumeTool("function_response", false, json.RawMessage(`{"id":"call-1","response":{"isError":true,"content":[{"text":"tool not permitted; approval request filed"}]}}`), &out)
	if !view.denied || !view.requestFiled {
		t.Fatalf("verified governed denial was not retained: %+v", view)
	}
	if strings.Contains(out.String(), "tool response observed") {
		t.Fatalf("denial was followed by a success-like receipt:\n%s", out.String())
	}
	for _, want := range []string{"[POSSIBLE KAIMAHI DENIAL]", "Signal: response text matches a Kaimahi denial", "Provenance: unverified", "Approval request: reported in response text"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("immediate denial output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestGovernedModelDenialIsVisibleFromFailedStatus(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, renderer: renderer, modelGoverned: true}
	status := json.RawMessage(`{"state":"failed","message":{"role":"agent","messageId":"m1","parts":[{"kind":"text","text":"monthly token budget reached; approval request filed"}]}}`)
	view.consume(streamEvent{Status: status}, &out)
	for _, want := range []string{
		"[POSSIBLE KAIMAHI DENIAL]",
		"Seam: model proxy",
		"Signal: response text matches a Kaimahi denial",
		"Provenance: unverified",
		"Reason: monthly token budget reached",
		"Approval request: reported in response text",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("model governance output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDirectModelFailureDoesNotClaimGovernance(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, renderer: renderer}
	status := json.RawMessage(`{"state":"failed","message":{"role":"agent","messageId":"m1","parts":[{"kind":"text","text":"monthly token budget reached"}]}}`)
	view.consume(streamEvent{Status: status}, &out)
	if strings.Contains(out.String(), "KAIMAHI") {
		t.Fatalf("direct model failure was presented as governed:\n%s", out.String())
	}
}

func TestNonAgentFailureTextDoesNotClaimGovernance(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := newStreamView("agent", "off", renderer, &chatGovernancePosture{modelGoverned: true})
	status := json.RawMessage(`{"state":"failed","message":{"role":"user","messageId":"m1","parts":[{"kind":"text","text":"monthly token budget reached"}]}}`)
	view.consume(streamEvent{Status: status}, &out)
	if strings.Contains(out.String(), "POSSIBLE KAIMAHI") {
		t.Fatalf("non-agent failure text was treated as a model response:\n%s", out.String())
	}
}

func TestToolGovernanceAttributionRequiresStableCallIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		calls    []json.RawMessage
		response json.RawMessage
	}{
		{"empty id", []json.RawMessage{json.RawMessage(`{"id":"","name":"post","args":{}}`)}, json.RawMessage(`{"id":"","name":"post","response":{"isError":true,"content":[{"text":"tool not permitted"}]}}`)},
		{"reused id", []json.RawMessage{json.RawMessage(`{"id":"call-1","name":"post","args":{}}`), json.RawMessage(`{"id":"call-1","name":"direct","args":{}}`)}, json.RawMessage(`{"id":"call-1","name":"post","response":{"isError":true,"content":[{"text":"tool not permitted"}]}}`)},
		{"mismatched response", []json.RawMessage{json.RawMessage(`{"id":"call-1","name":"post","args":{}}`)}, json.RawMessage(`{"id":"call-1","name":"direct","response":{"isError":true,"content":[{"text":"tool not permitted"}]}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			view := newStreamView("agent", "off", &chatRenderer{out: &out}, &chatGovernancePosture{governedTools: map[string]bool{"post": true}})
			for _, call := range tc.calls {
				view.consumeTool("function_call", false, call, &out)
			}
			view.consumeTool("function_response", false, tc.response, &out)
			if strings.Contains(out.String(), "POSSIBLE KAIMAHI") || view.denied {
				t.Fatalf("ambiguous call identity was attributed to Kaimahi: output=%q denied=%v", out.String(), view.denied)
			}
		})
	}
}

func TestGovernedToolRouteAndDenialAreDeduplicated(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	view := newStreamView("agent", "off", renderer, &chatGovernancePosture{governedTools: map[string]bool{"post": true}})
	call := json.RawMessage(`{"id":"call-1","name":"post","args":{}}`)
	response := json.RawMessage(`{"id":"call-1","response":{"isError":true,"content":[{"text":"tool not permitted"}]}}`)
	view.consumeTool("function_call", false, call, &out)
	view.consumeTool("function_call", false, call, &out)
	view.consumeTool("function_response", false, response, &out)
	view.consumeTool("function_response", false, response, &out)
	if got := strings.Count(out.String(), "[KAIMAHI ROUTE]"); got != 1 {
		t.Fatalf("route rendered %d times:\n%s", got, out.String())
	}
	if got := strings.Count(out.String(), "[POSSIBLE KAIMAHI DENIAL]"); got != 1 {
		t.Fatalf("denial signal rendered %d times:\n%s", got, out.String())
	}
}

func TestStreamLabelsDistinctAgentMessages(t *testing.T) {
	var out bytes.Buffer
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "summary"}
	for _, id := range []string{"one", "two"} {
		status := fmt.Sprintf(`{"state":"working","message":{"role":"agent","messageId":%q,"parts":[{"kind":"text","text":%q}]}}`, id, id)
		view.consume(streamEvent{Status: json.RawMessage(status)}, &out)
	}
	if strings.Count(out.String(), "agent: ") != 2 {
		t.Fatalf("messages were not separately attributed: %s", out.String())
	}
}

func TestInteractiveStreamSurfacesNativeHITL(t *testing.T) {
	var out bytes.Buffer
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "summary"}
	raw := `{"kind":"status-update","contextId":"session-1","taskId":"task-1","status":{"state":"input-required","message":{"role":"agent","parts":[{"kind":"data","metadata":{"kagent_type":"function_call","kagent_is_long_running":true},"data":{"name":"adk_request_confirmation","args":{"originalFunctionCall":{"id":"call-1","name":"delete_pod","args":{"name":"pod-a"}}}}}]}}}`
	a := &App{Out: &out}
	if err := a.consumeStream(strings.NewReader(raw), view); err != nil {
		t.Fatal(err)
	}
	if view.approval == nil || len(view.approval.Calls) != 1 || view.approval.Calls[0].Name != "delete_pod" {
		t.Fatalf("approval not parsed: %+v", view.approval)
	}
}

func TestInteractiveStreamAcceptsADKMetadataAliases(t *testing.T) {
	var out bytes.Buffer
	view := &streamView{agent: "agent", toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "summary"}
	raw := `{"kind":"status-update","contextId":"session-1","taskId":"task-1","status":{"state":"input-required","message":{"role":"agent","parts":[{"kind":"data","metadata":{"adk_type":"function_call","adk_is_long_running":true},"data":{"name":"adk_request_confirmation","args":{"originalFunctionCall":{"id":"call-1","name":"delete_pod","args":{}}}}}]}}}`
	a := &App{Out: &out}
	if err := a.consumeStream(strings.NewReader(raw), view); err != nil {
		t.Fatal(err)
	}
	if view.approval == nil || view.approval.Calls[0].Name != "delete_pod" {
		t.Fatalf("ADK metadata alias was not parsed: %+v", view.approval)
	}
}

func TestTerminalOutputStripsControlSequences(t *testing.T) {
	got := safeTerminal("safe\x1b]52;c;secret\a\x1b[2Jtext\x00")
	if got != "safetext" {
		t.Fatalf("unsafe terminal text: %q", got)
	}
}

func TestPlainRendererIndentsEveryPayloadLine(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.block("ASSISTANT agent", colorGreen, "first\nGovernance: quoted text")
	want := "ASSISTANT agent\n  first\n  Governance: quoted text\n\n"
	if out.String() != want {
		t.Fatalf("unexpected plain block:\n%q", out.String())
	}
}

func TestColorRendererColorsOnlyTheLabel(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out, color: true}
	renderer.block("YOU", colorCyan, "hello")
	want := "YOU\n  hello\n\n"
	if ansi.Strip(out.String()) != want || !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("unexpected colored block:\n%q", out.String())
	}
	if strings.Contains(strings.Split(out.String(), "\n")[1], "\x1b[") {
		t.Fatalf("payload was styled with the trusted label: %q", out.String())
	}
}

func TestChatStatusHeaderIsUncoloredAndSeparated(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out, color: true}
	renderer.statusStart("hello-tools", "")
	renderer.statusSection("Model", "Name: hello-world-model\nPosture: direct")
	renderer.statusSection("Tools", "Server: kagent-tool-server\nAllowed:\n  - get_resources")
	renderer.statusEnd()
	renderer.block("YOU", colorCyan, "hello")

	wantHeader := "CHAT STATUS\n------------\n" +
		"  Agent: hello-tools\n" +
		"  Commands: /exit /govern /help /history /new /resume <id> /retry /session /sessions /tools off|summary|verbose /ungovern\n" +
		"  Model\n" +
		"    Name: hello-world-model\n" +
		"    Posture: direct\n" +
		"  Tools\n" +
		"    Server: kagent-tool-server\n" +
		"    Allowed:\n" +
		"      - get_resources\n" +
		"------------------------------------------------------------\n\n"
	if !strings.HasPrefix(out.String(), wantHeader) {
		t.Fatalf("status header was colored or malformed:\n%q", out.String())
	}
	conversation := strings.TrimPrefix(out.String(), wantHeader)
	if !strings.Contains(conversation, "\x1b[") || ansi.Strip(conversation) != "YOU\n  hello\n\n" {
		t.Fatalf("conversation did not begin after the uncolored header:\n%q", out.String())
	}
}

func TestOperationalInteractionsDifferFromMessages(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.block("YOU", colorCyan, "list pods")
	renderer.operation("TOOL CALL", "get_pods", colorMagenta, "Status: running\nArguments:\n"+indentPayload(`{"namespace":"default"}`))
	renderer.operation("TOOL RESULT", "get_pods", colorMagenta, "Status: completed")
	renderer.operation("POSSIBLE KAIMAHI DENIAL", "post_message", colorYellow, "Signal: denial text\nProvenance: unverified")

	want := "YOU\n  list pods\n\n" +
		"[TOOL CALL]\n  Tool: get_pods\n  Status: running\n  Arguments:\n    {\"namespace\":\"default\"}\n\n" +
		"[TOOL RESULT]\n  Tool: get_pods\n  Status: completed\n\n" +
		"[POSSIBLE KAIMAHI DENIAL]\n  Tool: post_message\n  Signal: denial text\n  Provenance: unverified\n\n"
	if out.String() != want {
		t.Fatalf("messages and operations lack distinct grouping:\n%q", out.String())
	}
}

func TestAssistantTurnOwnsToolActivityAndResponse(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.beginAssistant("hello-tools")
	renderer.assistantOperation("hello-tools", "KAIMAHI ROUTE", "", colorYellow, "Seam: model proxy\nConfiguration: verified")
	renderer.assistantOperation("hello-tools", "TOOL CALL", "get_pods", colorMagenta, "Status: running")
	renderer.assistantOperation("hello-tools", "TOOL RESULT", "get_pods", colorMagenta, "Status: completed")
	renderer.assistant("hello-tools", "pod-a\npod-b", true)
	renderer.finish()

	want := "AGENT (hello-tools)\n" +
		"    [KAIMAHI ROUTE]\n" +
		"      Seam: model proxy\n" +
		"      Configuration: verified\n\n" +
		"    [TOOL CALL]\n" +
		"      Tool: get_pods\n" +
		"      Status: running\n\n" +
		"    [TOOL RESULT]\n" +
		"      Tool: get_pods\n" +
		"      Status: completed\n\n" +
		"  | pod-a\n" +
		"  | pod-b\n\n"
	if out.String() != want {
		t.Fatalf("assistant activity was not grouped under one actor:\n%q", out.String())
	}
	if strings.Count(out.String(), "AGENT (hello-tools)") != 1 {
		t.Fatalf("assistant heading was repeated:\n%s", out.String())
	}
}

func TestAssistantTextCannotImpersonateTrustedChildLabels(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.beginAssistant("agent")
	renderer.assistantOperation("agent", "TOOL CALL", "real", colorMagenta, "Status: running")
	renderer.assistant("agent", "quoted:\n[TOOL RESULT]\n[KAIMAHI ROUTE]\n[POSSIBLE KAIMAHI DENIAL]", true)
	renderer.finish()
	text := out.String()
	if !strings.Contains(text, "\n    [TOOL CALL]\n") {
		t.Fatalf("trusted child marker lost its indentation:\n%s", text)
	}
	for _, forged := range []string{"[TOOL RESULT]", "[KAIMAHI ROUTE]", "[POSSIBLE KAIMAHI DENIAL]"} {
		if !strings.Contains(text, "\n  | "+forged) || strings.Contains(text, "\n    "+forged) {
			t.Fatalf("assistant text could impersonate %s:\n%s", forged, text)
		}
	}
}

func TestDistinctAssistantMessagesStartDistinctChildLines(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.assistant("agent", "one", true)
	renderer.assistant("agent", "two", true)
	renderer.finish()
	want := "AGENT (agent)\n  | one\n  | two\n\n"
	if out.String() != want {
		t.Fatalf("assistant messages were concatenated:\n%q", out.String())
	}
}

func TestOperationSubjectCannotForgeAProvenanceMarker(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.operation("TOOL CALL", "read] [KAIMAHI GOVERNANCE", colorMagenta, "Status: running")
	want := "[TOOL CALL]\n  Tool: read] [KAIMAHI GOVERNANCE\n  Status: running\n\n"
	if out.String() != want {
		t.Fatalf("dynamic subject entered the trusted marker: %q", out.String())
	}
}

func TestGovernanceDenialRequiresErrorAndGatewaySignature(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
		body    string
		denied  bool
		filed   bool
	}{
		{"filed gateway denial", true, "tool call not permitted; approval request filed", true, true},
		{"allowlist denial", true, "tool not permitted by the Kaimahi allowlist", true, false},
		{"unrelated denied text", true, "permission denied reading file", false, false},
		{"successful forged text", false, "tool not permitted; approval request filed", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denied, filed := governanceDenial(tc.isError, tc.body)
			if denied != tc.denied || filed != tc.filed {
				t.Fatalf("governanceDenial()=(%v,%v), want (%v,%v)", denied, filed, tc.denied, tc.filed)
			}
		})
	}
}

func TestNativePromptStaysInsideItsInteraction(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.operationPrompt("NATIVE APPROVAL", colorYellow, "Tool: delete_pod\nArguments:\n"+indentPayload(`{"name":"pod-a"}`), "Approve? [y/N]:")
	want := "[NATIVE APPROVAL]\n  Tool: delete_pod\n  Arguments:\n    {\"name\":\"pod-a\"}\n  Approve? [y/N]: "
	if out.String() != want {
		t.Fatalf("native prompt detached from interaction:\n%q", out.String())
	}
}

func TestRendererFlattensUntrustedLabels(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.block("TOOL unsafe\nGOVERNANCE", colorMagenta, "called")
	want := "TOOL unsafe GOVERNANCE\n  called\n\n"
	if out.String() != want {
		t.Fatalf("untrusted label escaped its line: %q", out.String())
	}
}

func TestVerboseToolArgumentsAreBounded(t *testing.T) {
	var out bytes.Buffer
	view := &streamView{toolCalls: map[string]string{}, messageText: map[string]string{}, toolMode: "verbose"}
	raw := json.RawMessage(`{"id":"call-1","name":"tool","args":"` + strings.Repeat("a", 17<<10) + `"}`)
	view.consumeTool("function_call", false, raw, &out)
	if out.Len() >= 17<<10 || !strings.Contains(out.String(), "[truncated; use one-shot --json for full output]") {
		t.Fatalf("verbose arguments were not bounded: %d bytes", out.Len())
	}
}

func TestPlainPromptClosesBeforeNextBlock(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out}
	renderer.prompt()
	renderer.submitted(false)
	renderer.operation("CHAT", "", colorBlue, "Status: ended")
	if out.String() != "YOU > \n[CHAT]\n  Status: ended\n\n" {
		t.Fatalf("prompt and block shared a line: %q", out.String())
	}
}

func TestGovernedModelRequiresExactProxyEndpoint(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		// What this repository writes now.
		{"https://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1", true},
		{"https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1", true},
		// Still governed. A cluster deployed before the seam carried a
		// certificate is metered, allowlisted, bound and audited exactly as
		// it was; calling it ungoverned would report an enforcing plane as
		// one that is not. What plaintext costs is confidentiality of the
		// content on the wire, which `kmx status` reports separately.
		{"http://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1", true},
		{"http://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1", true},
		// The host is still exact: a scheme that is not a seam's, a host
		// that only CONTAINS the seam's name, and an unparseable URL are
		// all somebody else's endpoint.
		{"ftp://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1", false},
		{"http://example.invalid/kaimahi-proxy.kaimahi:8080/upstream/x", false},
		{"https://kaimahi-proxy.kaimahi:9999/upstream/ollama/v1", false},
		{"http://[::1", false},
	} {
		if got := usesKaimahiModelProxy(map[string]any{"openAI": map[string]any{"baseUrl": tc.url}}); got != tc.want {
			t.Errorf("usesKaimahiModelProxy(%q)=%v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestGovernedModelSearchContinuesPastOtherBaseURLs(t *testing.T) {
	spec := map[string]any{
		"direct": map[string]any{"baseUrl": "https://example.invalid/v1"},
		"nested": []any{map[string]any{"base_url": "http://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1"}},
	}
	for i := 0; i < 100; i++ {
		if !usesKaimahiModelProxy(spec) {
			t.Fatal("a nonmatching baseUrl stopped discovery of the governed endpoint")
		}
	}
}

// With the opt-out set, a missing tool is reported rather than fetched — and
// every missing one is reported at once, so a first run does not discover
// them one install at a time.
func TestUpPreflightReportsAllMissingDependencies(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker"}, Run: &run.Runner{}}
	err := a.preflightUp([]string{"cluster", "kagent"})
	if err == nil {
		t.Fatal("preflight unexpectedly passed")
	}
	for _, want := range []string{"3 missing or unusable dependencies", "kind is not on PATH", "kubectl is not on PATH", "helm is not on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight error lacks %q:\n%s", want, err)
		}
	}
}

func TestMissingChatAgentFailsBeforeServabilityPoll(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `
case "$*" in
  *"get agent hello-tool -o name"*)
    echo 'Error from server (NotFound): agents.kagent.dev "hello-tool" not found' >&2
    exit 1
    ;;
  *"get agents -o name"*)
    printf 'agent.kagent.dev/hello-tools\nagent.kagent.dev/hello-world\n'
    exit 0
    ;;
esac
exit 99`)
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out,
		Err: &out,
	}
	started := time.Now()
	err := a.waitServable("hello-tool")
	if err == nil {
		t.Fatal("missing agent unexpectedly became servable")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("missing agent did not fail immediately: %s", time.Since(started))
	}
	for _, want := range []string{`agent "hello-tool" does not exist`, "available agents: hello-tools, hello-world"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestInteractiveMutationRequiresRemotePreconfirmation(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `
if [ "$*" = "config view -o json" ]; then
  printf '{"contexts":[{"name":"prod","context":{"cluster":"prod"}}],"clusters":[{"name":"prod","cluster":{"server":"https://prod.example"}}]}'
  exit 0
fi
exit 99`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KubeContext: "prod"}, Run: &run.Runner{}}
	err := a.prepareInteractiveMutation()
	if err == nil || !strings.Contains(err.Error(), "KAIMAHI_CONFIRM=prod before chat starts") {
		t.Fatalf("remote mutation did not require preconfirmation: %v", err)
	}
	a.Cfg.Confirm = "prod"
	if err := a.prepareInteractiveMutation(); err != nil {
		t.Fatalf("matching preconfirmation was refused: %v", err)
	}
}

func TestValidateToolServer(t *testing.T) {
	wiring := &scaffold.ToolWiring{Server: "tools", Tools: []string{"get", "events"}}
	accepted := []serverCondition{{Type: "Accepted", Status: "True", ObservedGeneration: 2}}
	if err := validateToolServer(wiring, 2, 2, accepted, map[string]bool{"get": true, "events": true}); err != nil {
		t.Fatal(err)
	}
	if err := validateToolServer(wiring, 2, 2, accepted, map[string]bool{"get": true}); err == nil || !strings.Contains(err.Error(), "events") {
		t.Fatalf("missing tool was not reported: %v", err)
	}
	if err := validateToolServer(wiring, 2, 2, []serverCondition{{Type: "Accepted", Status: "False", Message: "dial failed", ObservedGeneration: 2}}, nil); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("unaccepted server was not reported: %v", err)
	}
	if err := validateToolServer(wiring, 2, 1, accepted, nil); err == nil || !strings.Contains(err.Error(), "still reconciling") {
		t.Fatalf("stale discovery was not refused: %v", err)
	}
}
