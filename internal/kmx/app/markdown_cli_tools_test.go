package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"go.yaml.in/yaml/v3"
)

const cliToolAgent = "---\nname: cluster-reader\ndescription: Read approved development clusters\ntools: [get_cluster]\n---\nUse get_cluster to inspect a development cluster.\n\n## Tool bindings\n\n```yaml kmx-tool\nname: get_cluster\ndescription: Read a dg cluster\ncommand: fixture-cli\nargs:\n  - show\n  - --name\n  - argument: cluster\nparameters:\n  cluster:\n    type: string\n    pattern: '^dg-[a-z0-9-]+$'\n    max-length: 63\nexecution:\n  timeout: 2s\n  max-output-bytes: 1024\n  approval: granted-scope\nvalidation:\n  args: [version]\n  exit-code: 0\n  stdout-pattern: '^fixture 2\\.[0-9]+\\n?$'\n  timeout: 1s\n```\n"

func cliFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "reader.agent.md")
	if err := os.WriteFile(path, []byte(strings.Split(cliToolAgent, "\n## Tool bindings")[0]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, specRaw, _ := strings.Cut(cliToolAgent, "```yaml kmx-tool\n")
	specRaw = strings.TrimSuffix(specRaw, "```\n")
	var spec CLIToolSpec
	if err := yaml.Unmarshal([]byte(specRaw), &spec); err != nil {
		t.Fatal(err)
	}
	b, err := yaml.Marshal(markdownToolsFile{SchemaVersion: 1, Tools: map[string]CLIToolSpec{spec.Name: spec}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MarkdownToolsPath(path), b, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	return path, bin
}

func TestCLIFileValidationIsOfflineAndStripsBindings(t *testing.T) {
	path, bin := cliFixture(t)
	marker := filepath.Join(t.TempDir(), "calls")
	t.Setenv("KMX_TEST_CALLS", marker)
	fakeTool(t, bin, "fixture-cli", `printf '%s\n' "$*" >> "$KMX_TEST_CALLS"`)
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.CLITools) != 1 || strings.Contains(a.Instructions, "Tool bindings") || strings.Contains(a.Instructions, "stdout-pattern") {
		t.Fatalf("binding leaked: %+v", a)
	}
	p, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Grant.CLI[0].Executable != filepath.Join(bin, "fixture-cli") {
		t.Fatal("PATH not resolved")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline validation/review executed probe")
	}
	if g, _, err := readCurrentToolGrant(a.Path); err != nil || g != nil {
		t.Fatal("review granted permission")
	}
	for _, raw := range []string{
		strings.Replace(cliToolAgent, "command: fixture-cli", "command: /bin/sh", 1),
		strings.Replace(cliToolAgent, "'^dg-[a-z0-9-]+$'", "'dg-.*'", 1),
		strings.Replace(cliToolAgent, "argument: cluster", "argument: unknown", 1),
		strings.Replace(cliToolAgent, "args: [version]", "args: [{argument: cluster}]", 1),
		strings.Replace(cliToolAgent, "exit-code: 0", "exit-code: 0\n  script: bad", 1),
		strings.Replace(cliToolAgent, "approval: granted-scope", "approval: once-per-session", 1),
	} {
		if _, err := parseMarkdownAgent(path, []byte(raw)); err == nil {
			t.Fatal("invalid CLI binding accepted")
		}
	}
}

func TestCLIApprovalProbeAndArgumentEnforcement(t *testing.T) {
	path, bin := cliFixture(t)
	marker := filepath.Join(t.TempDir(), "calls")
	t.Setenv("KMX_TEST_CALLS", marker)
	fakeTool(t, bin, "fixture-cli", `printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
if [ "$1" = version ]; then printf 'fixture 2.4\n'; else printf '%s' '{"cluster":"dg-test","ready":true}'; fi`)
	p, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyCLIToolApproval(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = attachApprovedTools(a); err != nil {
		t.Fatal(err)
	}
	if len(p.ProbeResults) != 1 || p.ProbeResults[0] != "get_cluster: passed" {
		t.Fatal(p.ProbeResults)
	}
	before, _ := os.ReadFile(marker)
	for _, args := range []any{map[string]any{"cluster": "draft-test"}, map[string]any{"cluster": "--help"}, map[string]any{"cluster": "dg-test;touch x"}, map[string]any{"cluster": "dg-test", "command": "rm"}, map[string]any{"cluster": "dg-$(id)"}} {
		if _, err := executeCLITool(t.Context(), a.Grant.CLI[0], args); err == nil {
			t.Fatal("invalid argument executed")
		}
	}
	after, _ := os.ReadFile(marker)
	if !bytes.Equal(before, after) {
		t.Fatal("denied tool started binary")
	}
	result, err := executeCLITool(t.Context(), a.Grant.CLI[0], map[string]any{"cluster": "dg-test"})
	if err != nil || !strings.Contains(result, "dg-test") {
		t.Fatalf("result=%s err=%v", result, err)
	}
	after, _ = os.ReadFile(marker)
	if !strings.Contains(string(after), "show --name dg-test") {
		t.Fatal(string(after))
	}
	frozen, err := CacheMarkdownVersion(a)
	if err != nil {
		t.Fatal(err)
	}
	if err = loadToolSnapshot(frozen, a.BindingDigest); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	fakeTool(t, other, "fixture-cli", `printf 'fixture 2.4\n'`)
	t.Setenv("PATH", other)
	if _, err = executeCLITool(t.Context(), a.Grant.CLI[0], map[string]any{"cluster": "dg-test"}); err == nil {
		t.Fatal("PATH change did not invalidate binding")
	}
}

func TestCLIProbeFailureNeverGrants(t *testing.T) {
	for _, body := range []string{`printf 'fixture 1.0\n'`, `printf 'fixture 2.4\n'; exit 2`, `printf '%020000d' 0`} {
		path, bin := cliFixture(t)
		fakeTool(t, bin, "fixture-cli", body)
		p, err := PrepareCLIToolApproval(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = ApplyCLIToolApproval(t.Context(), &p); err == nil {
			t.Fatal("failed probe granted tool")
		}
		if g, _, err := readCurrentToolGrant(p.Grant.Source); err != nil || g != nil {
			t.Fatal("failed probe wrote grant")
		}
	}
}

func TestCLIProbeCancellationAndSourceChange(t *testing.T) {
	path, bin := cliFixture(t)
	fakeTool(t, bin, "fixture-cli", `while :; do :; done`)
	p, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err = ApplyCLIToolApproval(ctx, &p); err == nil {
		t.Fatal("cancelled probe passed")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("probe did not stop promptly")
	}
	raw, _ := os.ReadFile(path)
	if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCLIToolApproval(t.Context(), &p); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("stale review=%v", err)
	}
}

func TestCLIMocksNeedNoInstalledBinary(t *testing.T) {
	path, _ := cliFixture(t)
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	// Pure parsing/model-facing schema generation cannot require PATH installation.
	schema := cliToolParameters(a.CLITools[0])
	if schema["additionalProperties"] != false {
		t.Fatal("schema open")
	}
	_, _, err = readCurrentToolGrant(a.Path)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCLIHandlerReportsNonzeroOutputAsFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"test failure", `printf '--- FAIL: TestAdd\n'; printf 'expected 4, got 5\n' >&2; exit 1`, "expected 4, got 5"},
		{"compiler failure", `printf 'main.go:8: undefined: missing\n' >&2; exit 2`, "main.go:8: undefined: missing"},
		{"terminal controls", `printf '\033[31mfailed\033[0m\n' >&2; exit 1`, "failed"},
		{"no output", `exit 3`, "tool exited with code 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, bin := cliFixture(t)
			fakeTool(t, bin, "fixture-cli", tc.body)
			plan, err := PrepareCLIToolApproval(path)
			if err != nil {
				t.Fatal(err)
			}
			var budget atomic.Int32
			budget.Store(8)
			tool := cliReadTools(plan.Grant, &budget)[0]
			result, err := tool.Handler(copilot.ToolInvocation{Arguments: map[string]any{"cluster": "dg-test"}, TraceContext: t.Context()})
			if err != nil || result.ResultType != "failure" || !strings.Contains(result.TextResultForLLM, tc.want) || strings.Contains(result.TextResultForLLM, "\x1b") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCLIOutputWithholdsUnsafeAndIncompleteResults(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"stdout secret", `printf 'ghp_'; printf 'abcdefghijklmnopqrstuvwxyz1234567890'; exit 1`, "credential-shaped"},
		{"stderr secret on success", `printf 'safe'; printf 'ghp_' >&2; printf 'abcdefghijklmnopqrstuvwxyz1234567890' >&2`, "credential-shaped"},
		{"normalized secret", `printf 'ghp_\033[31m'; printf 'abcdefghijklmnopqrstuvwxyz1234567890'; exit 1`, "credential-shaped"},
		{"invalid bytes", `printf '\377'; exit 1`, "invalid UTF-8"},
		{"combined budget", `printf '%0600d' 0; printf '%0600d' 0 >&2; exit 1`, "output limit"},
		{"timeout", `printf 'partial diagnostic'; while :; do :; done`, "timed out or was cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, bin := cliFixture(t)
			fakeTool(t, bin, "fixture-cli", tc.body)
			plan, err := PrepareCLIToolApproval(path)
			if err != nil {
				t.Fatal(err)
			}
			tool := plan.Grant.CLI[0]
			tool.Spec.Execution.Timeout = "100ms"
			text, err := executeCLITool(t.Context(), tool, map[string]any{"cluster": "dg-test"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || text != "" || strings.Contains(err.Error(), "partial diagnostic") {
				t.Fatalf("text=%q err=%v", text, err)
			}
		})
	}
}

func TestCLIProbeMatchesStdoutOnly(t *testing.T) {
	path, bin := cliFixture(t)
	fakeTool(t, bin, "fixture-cli", `printf 'fixture 2.4\n' >&2`)
	plan, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &plan); err == nil {
		t.Fatal("stderr satisfied stdout-only probe")
	}
}

func TestCLIHandlerPreservesSuccessfulStdoutAndReportsStderr(t *testing.T) {
	for _, diagnostics := range []bool{false, true} {
		path, bin := cliFixture(t)
		body := `printf '{"ok":true}'`
		if diagnostics {
			body += `; printf 'compiler warning\n' >&2`
		}
		fakeTool(t, bin, "fixture-cli", body)
		plan, err := PrepareCLIToolApproval(path)
		if err != nil {
			t.Fatal(err)
		}
		var budget atomic.Int32
		budget.Store(8)
		result, err := cliReadTools(plan.Grant, &budget)[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"cluster": "dg-test"}, TraceContext: t.Context()})
		if err != nil || result.ResultType != "success" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if !diagnostics && result.TextResultForLLM != `{"ok":true}` {
			t.Fatal("stdout-only success changed")
		}
		if diagnostics && (!strings.Contains(result.TextResultForLLM, "stderr:\ncompiler warning") || !strings.Contains(result.TextResultForLLM, `{"ok":true}`)) {
			t.Fatal("successful command lost stream content")
		}
	}
}

func TestCLIGoTestFailurePreservesDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable unavailable")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":       "module example.invalid/tool-demo\n\ngo 1.24\n",
		"demo_test.go": "package demo\nimport \"testing\"\nfunc TestAdd(t *testing.T) { t.Fatal(\"expected 4, got 5\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := ApprovedCLITool{Spec: CLIToolSpec{Name: "go_test", Description: "Run the fixture tests", Command: "go", Args: []CLIToolArg{{Literal: "-C"}, {Literal: root}, {Literal: "test"}, {Literal: "-count=1"}, {Literal: "./..."}}, Parameters: map[string]CLIToolParameter{}}}
	tool.Spec.Execution.Timeout, tool.Spec.Execution.MaxOutputBytes, tool.Spec.Execution.Approval = "30s", 16384, "granted-scope"
	var err error
	tool.Executable, tool.ExecutableDigest, err = resolveCLICommand("go")
	if err != nil {
		t.Fatal(err)
	}
	text, err := executeCLITool(t.Context(), tool, map[string]any{})
	var exit *cliCommandExitError
	if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(text, "--- FAIL: TestAdd") || !strings.Contains(text, "expected 4, got 5") {
		t.Fatalf("Go test diagnostics lost: text=%q err=%v", text, err)
	}
	t.Logf("%v\n%s", err, text)
}
