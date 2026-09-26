package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func fakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
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
	renderer.statusSection("Tools", "Server: cluster-tool-server\nAllowed:\n  - get_resources")
	renderer.statusEnd()
	renderer.block("YOU", colorCyan, "hello")

	wantHeader := "CHAT STATUS\n------------\n" +
		"  Agent: hello-tools\n" +
		"  Commands: " + slashCommandSummary() + "\n" +
		"  Model\n" +
		"    Name: hello-world-model\n" +
		"    Posture: direct\n" +
		"  Tools\n" +
		"    Server: cluster-tool-server\n" +
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

// With the opt-out set, a missing tool is reported rather than fetched — and
// every missing one is reported at once, so a first run does not discover
// them one install at a time.
func TestUpPreflightReportsAllMissingDependencies(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker"}, Run: &run.Runner{}}
	err := a.preflightUp([]string{"cluster"})
	if err == nil {
		t.Fatal("preflight unexpectedly passed")
	}
	for _, want := range []string{"2 missing or unusable dependencies", "kind is not on PATH", "kubectl is not on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight error lacks %q:\n%s", want, err)
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
