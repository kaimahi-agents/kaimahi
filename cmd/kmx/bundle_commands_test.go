package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var bundleFixture = filepath.Join("..", "..", "internal", "kmx", "bundle", "testdata", "research-team")

func runBundle(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute(args, deps)
	if *loads != 0 {
		t.Fatalf("%v loaded operational config %d time(s)", args, *loads)
	}
	return out.String(), err
}

func TestBundleValidateText(t *testing.T) {
	out, err := runBundle(t, "bundle", "validate", bundleFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Bundle research-team 0.1.0: valid (providers=1 tools=2 agents=2)\n",
		"  Agent researcher sha256:",
		"    provider: default-chat\n    tools: web-search, fetch-page\n    allowedAgents: summarizer\n",
		"  Agent summarizer sha256:",
		"    tools: fetch-page\n    allowedAgents: none\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestBundleValidateJSON(t *testing.T) {
	out, err := runBundle(t, "bundle", "validate", bundleFixture, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report bundleReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if report.Name != "research-team" || len(report.Providers) != 1 || len(report.Tools) != 2 || len(report.Agents) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if a := report.Agents[1]; a.Name != "summarizer" || a.AllowedAgents == nil || len(a.AllowedAgents) != 0 {
		t.Fatalf("summarizer = %+v", a)
	}
	if report.Agents[0].Identity == report.Agents[1].Identity {
		t.Fatal("Agents share an identity")
	}
}

func TestBundleValidateRefusesInvalidBundle(t *testing.T) {
	dir := copySuiteFixture(t, bundleFixture)
	path := filepath.Join(dir, "agents", "summarizer.yaml")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(data, []byte("default-chat"), []byte("missing-chat"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runBundle(t, "bundle", "validate", dir)
	if err == nil || !strings.Contains(err.Error(), `Agent "summarizer" uses unknown Provider "missing-chat"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestBundleRenderWholeBundle(t *testing.T) {
	out, err := runBundle(t, "bundle", "render", bundleFixture, "--namespace", "research")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Rendered by kmx bundle render from research-team 0.1.0.\n" +
		"# Required Secrets (name/key), not created here: local-model/api-key, search-credentials/api-key\n" +
		"apiVersion: core.orka.ai/v1alpha1\nkind: Provider\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("output does not start with %q:\n%s", want, out)
	}
	// One Provider and two Tools shared by two Agents render once each.
	for kind, n := range map[string]int{"Provider": 1, "Tool": 2, "Agent": 2} {
		if got := strings.Count(out, "\nkind: "+kind+"\n"); got != n {
			t.Errorf("rendered %d %s documents, want %d:\n%s", got, kind, n, out)
		}
	}
}

func TestBundleRenderRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"bundle", "render", bundleFixture}, "--namespace is required"},
		{[]string{"bundle", "render", bundleFixture, "-n", "research", "--orka-schema", "v9"}, "unknown offline Orka schema"},
		{[]string{"bundle", "render"}, "usage: kmx bundle render"},
	} {
		if _, err := runBundle(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// v0.2.0 removed Provider rateLimit, so rendering refuses it there rather than
// emitting YAML the installed CRD would reject.
func TestBundleRenderChecksTheSelectedOrkaSchema(t *testing.T) {
	dir := copySuiteFixture(t, bundleFixture)
	path := filepath.Join(dir, "providers", "default-chat.yaml")
	data, _ := os.ReadFile(path)
	limited := bytes.Replace(data, []byte("  credentials:"), []byte("  rateLimits:\n    requestsPerMinute: 60\n  credentials:"), 1)
	if err := os.WriteFile(path, limited, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runBundle(t, "bundle", "render", dir, "-n", "research"); err == nil || !strings.Contains(err.Error(), "Provider default-chat does not match Orka v0.2.0") {
		t.Fatalf("v0.2.0: err = %v", err)
	}
	out, err := runBundle(t, "bundle", "render", dir, "-n", "research", "--orka-schema", "v0.1.3")
	if err != nil {
		t.Fatalf("v0.1.3: %v", err)
	}
	if !strings.Contains(out, "rateLimit:\n        requestsPerMinute: 60") {
		t.Fatalf("rateLimit not rendered:\n%s", out)
	}
}
