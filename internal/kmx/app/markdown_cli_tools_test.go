package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cliToolAgent = "---\nname: cluster-reader\ndescription: Read approved development clusters\ntools: [get_cluster]\n---\nUse get_cluster to inspect a development cluster.\n\n## Tool bindings\n\n```yaml kmx-tool\nname: get_cluster\ndescription: Read a dg cluster\ncommand: fixture-cli\nargs:\n  - show\n  - --name\n  - argument: cluster\nparameters:\n  cluster:\n    type: string\n    pattern: '^dg-[a-z0-9-]+$'\n    max-length: 63\nexecution:\n  timeout: 2s\n  max-output-bytes: 1024\n  approval: granted-scope\nvalidation:\n  args: [version]\n  exit-code: 0\n  stdout-pattern: '^fixture 2\\.[0-9]+\\n?$'\n  timeout: 1s\n```\n"

func cliFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "reader.agent.md")
	if err := os.WriteFile(path, []byte(cliToolAgent), 0600); err != nil {
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
	if err = os.WriteFile(path, append([]byte(cliToolAgent), '\n'), 0600); err != nil {
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
