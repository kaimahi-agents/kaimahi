package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"go.yaml.in/yaml/v3"
)

func TestCompanionApprovedCommandHostReportsExitStatus(t *testing.T) {
	path, bin := cliFixture(t)
	fakeTool(t, bin, "fixture-cli", `if [ "$1" = version ]; then printf 'fixture 2.4\n'; else printf 'expected 4, got 5\n' >&2; exit 1; fi`)
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := loadPreviewCompanions(parsed, path)
	if err != nil {
		t.Fatal(err)
	}
	if host, err := snapshot.resolveTools(t.Context(), []string{"get_cluster"}); err != nil || host != nil {
		t.Fatal("unapproved command exposed", err)
	}
	plan, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	host, err := snapshot.resolveTools(t.Context(), []string{"get_cluster"})
	if err != nil || host == nil {
		t.Fatal("approved command missing", err)
	}
	result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call-1", Tool: "get_cluster", Args: map[string]any{"cluster": "dg-test"}, Mediation: api.MediationControllerMediated, IdempotencyKey: "session:call-1"})
	if err != nil || result.Output["succeeded"] != false || result.Output["exitCode"] != float64(1) || !strings.Contains(result.Output["output"].(string), "expected 4, got 5") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := os.WriteFile(path, append(snapshot.agent.Source, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call-2", Tool: "get_cluster", Args: map[string]any{"cluster": "dg-test"}, Mediation: api.MediationControllerMediated, IdempotencyKey: "session:call-2"}); err == nil {
		t.Fatal("source drift allowed execution")
	}
}

func TestCompanionCanonicalRunnerExecutesApprovedCommandWithoutInstance(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "ping.agent.md")
	writeFile(t, path, "---\nname: ping\nmodel: fake\ntools: [ping]\n---\nCall the tool `ping`. Reply with exactly `done`.\n")
	spec := CLIToolSpec{Description: "Check fixture", Command: "fixture-cli", Args: []CLIToolArg{{Literal: "test"}}, Parameters: map[string]CLIToolParameter{}}
	spec.Execution.Timeout = "5s"
	spec.Execution.MaxOutputBytes = 1024
	spec.Execution.Approval = "granted-scope"
	b, err := yaml.Marshal(markdownToolsFile{SchemaVersion: 1, Tools: map[string]CLIToolSpec{"ping": spec}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, MarkdownToolsPath(path), string(b))
	writeFile(t, MarkdownTestsPath(path), "---\nschemaVersion: 1\n---\n```yaml agent-test\nid: calls\ninput: Run it\nmocks:\n  tools:\n    - name: ping\n      description: Check fixture\n      parameters: {type: object, properties: {}, additionalProperties: false}\n      calls:\n        - arguments: {}\n          result: {ok: true}\nassertions:\n  - {id: answered, type: contains, value: done}\n```\n")
	bin := t.TempDir()
	marker := filepath.Join(root, "called")
	t.Setenv("KMX_MARKER", marker)
	fakeTool(t, bin, "fixture-cli", `printf ran > "$KMX_MARKER"; printf 'test failed\n' >&2; exit 1`)
	t.Setenv("PATH", bin)
	a := &App{FileRunner: NewLocalFileRunner()}
	opt := FilePreviewOptions{File: path, Operation: "test", Route: &modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}}
	r, err := a.PreviewAgentFile(t.Context(), opt)
	if err != nil || !r.FullPass || r.SnapshotDigest == "" {
		t.Fatalf("recorded preview: %+v %v", r, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("recorded test executed binary")
	}
	plan, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	input := "Run it"
	opt.Operation = "run"
	opt.Input = &input
	opt.AdHocOnly = true
	r, err = a.PreviewAgentFile(t.Context(), opt)
	if err != nil || r.AdHoc == nil || r.AdHoc.ToolCalls != 1 {
		t.Fatalf("command preview: %+v %v", r, err)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "ran" {
		t.Fatal("approved command not executed", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "agents", "instances")); !os.IsNotExist(err) {
		t.Fatal("preview started persistent instance")
	}
}

func TestCompanionRecordedCasesUseCanonicalObjects(t *testing.T) {
	path, _ := cliFixture(t)
	text := "---\nschemaVersion: 1\n---\n```yaml agent-test\nid: recorded\ninput: Read cluster\nmocks:\n  tools:\n    - name: get_cluster\n      description: Read cluster\n      parameters: {type: object, properties: {cluster: {type: string}}, required: [cluster], additionalProperties: false}\n      calls:\n        - arguments: {cluster: dg-test}\n          result: {ready: true}\nassertions:\n  - {id: called, type: toolCalled, tool: get_cluster}\n```\n"
	if err := os.WriteFile(MarkdownTestsPath(path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	combined, snapshot, err := loadPreviewCompanions(parsed, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(combined.Examples) != 1 || combined.Examples[0].Mocks.Tools[0].Mode != "recorded" || combined.Examples[0].Location.File != MarkdownTestsPath(path) {
		t.Fatal("recorded case not composed")
	}
	old := snapshot.digest
	if err := os.WriteFile(MarkdownTestsPath(path), []byte(text+"\ncomment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err = loadPreviewCompanions(parsed, path)
	if err != nil || snapshot.digest == old {
		t.Fatal("test edit did not change identity", err)
	}
}
