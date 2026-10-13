package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func orkaCreateArgs() []string {
	return []string{"create", "--live", "sample", "--namespace", "orka-system", "--provider-type", "openai", "--model", "qwen2.5:3b", "--secret", "local-provider-key", "--base-url", "http://ollama.ollama.svc.cluster.local:11434/v1", "--out", "-"}
}

func TestAgentCreateOrkaOfflineWithoutToolsOnPATH(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("broken: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute(orkaCreateArgs(), deps); err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(&out)
	for _, kind := range []string{"Secret", "Provider", "Agent"} {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		if doc["kind"] != kind {
			t.Fatalf("got %v, want %s", doc, kind)
		}
		if kind == "Provider" && doc["spec"].(map[string]any)["defaultModel"] != "qwen2.5:3b" {
			t.Fatal(doc)
		}
	}
	if !strings.Contains(diagnostics.String(), "not applied") {
		t.Fatal(diagnostics.String())
	}
}

func TestAgentCreateBundlePathEnablesBundleWithStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents", "sample")
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	args := append(orkaCreateArgs(), "--bundle-path", path)
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kind: Provider") {
		t.Fatal("stdout artifact missing")
	}
	if _, err := os.Stat(filepath.Join(path, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "bindings.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCreateRejectsLegacyFlagsAndSyntax(t *testing.T) {
	for _, extra := range [][]string{{"--image", "example/image"}, {"--isolation", "none"}, {"--run-as-user", "1000"}, {"--tools", "server:tool"}, {"--agent-requests-per-minute", "0"}, {"--provider-tokens-per-minute", "0"}, {"--dry-run"}} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		if err := execute(append(orkaCreateArgs(), extra...), deps); err == nil {
			t.Fatalf("accepted %v", extra)
		}
		if out.Len() != 0 {
			t.Fatalf("emitted bytes for invalid flags %v", extra)
		}
	}
}

func TestAgentCreateHelpExplainsOrkaBoundary(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Orka", "Provider", "watches", "ServiceAccount", "v0.2.0 (default)", "v0.1.3", "full", "--schema-target", "--skills"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help lacks %q", text)
		}
	}
	if strings.Contains(out.String(), "v0.1.3 (default)") || strings.Contains(out.String(), "Offline output uses pinned v0.1.3 CRDs") {
		t.Fatal("offline help still advertises the retired default")
	}
	if *loads != 0 {
		t.Fatal("help loaded config")
	}
}

func TestAgentCreateHelpExplainsGuidedModeAndStdoutBundle(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"With --live, omit the name for guided Orka creation",
		"Live named creation applies to the selected cluster by default",
		"--out - writes YAML to stdout without applying",
		"set --bundle-path to also write an authored bundle",
		"kmx create --live",
		"--base-url http://ollama.ollama.svc.cluster.local:11434/v1 --out -",
	} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help lacks %q", text)
		}
	}
	if strings.Contains(out.String(), "stdout requires this flag") {
		t.Fatal("help incorrectly requires bundle persistence for stdout")
	}
	if *loads != 0 {
		t.Fatal("help loaded config")
	}
}

func TestAgentHelpUsesLowercaseKagent(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"create", "--help"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if len(args) == 1 {
			if !strings.Contains(out.String(), "Author an offline workspace member, or create live Orka or kagent agents") {
				t.Errorf("root help does not use lowercase kagent in the create summary:\n%s", out.String())
			}
		} else if !strings.Contains(out.String(), "kagent") || strings.Contains(out.String(), "Kagent") {
			t.Errorf("create help does not use lowercase kagent consistently:\n%s", out.String())
		}
		if *loads != 0 {
			t.Fatal("help loaded config")
		}
	}
}

func TestAgentCreateHelpScopesRuntimeRequirements(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	_, orka, found := strings.Cut(out.String(), "Orka creation:\n")
	if !found {
		t.Fatal("help does not identify Orka-specific requirements")
	}
	orka, kagent, found := strings.Cut(orka, "kagent creation:\n")
	if !found {
		t.Fatal("help does not identify kagent-specific requirements")
	}
	kagent, _, _ = strings.Cut(kagent, "\nUsage:")
	for _, text := range []string{"ServiceAccount", "Applying --task", "Offline output uses pinned v0.2.0 CRDs", "Dry-run tests neither access nor execution"} {
		if !strings.Contains(orka, text) {
			t.Errorf("Orka help lacks %q", text)
		}
	}
	for _, text := range []string{"v0.10.2", "--description", "ModelConfig", "stores the full task prompt, history, and answer"} {
		if !strings.Contains(kagent, text) {
			t.Errorf("kagent help lacks %q", text)
		}
	}
	if strings.Contains(kagent, "ServiceAccount") || strings.Contains(kagent, "Offline output uses pinned v0.2.0 CRDs") {
		t.Fatal("kagent help applies Orka-specific prerequisites")
	}
	if *loads != 0 {
		t.Fatal("help loaded config")
	}
}

func TestAgentCreateHelpDisclosesKagentRetentionAndAuditRisk(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"stores the full task prompt, history, and answer",
		"default upstream session retention is unlimited",
		"audit policy may", "Service-proxy request and response bodies",
	} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help lacks %q", text)
		}
	}
	if *loads != 0 {
		t.Fatal("help loaded config")
	}
}

func TestAgentCreateUnnamedReachesWizardRatherThanRequiredFlags(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	err := execute([]string{"create", "--live"}, deps)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("wizard unreachable: %v", err)
	}
}

func kagentCreateArgs() []string {
	return []string{"create", "--live", "sample", "--runtime", "kagent", "--namespace", "agents", "--description", "Sample agent", "--provider-type", "openai", "--model", "gpt-4o-mini", "--secret", "model-key", "--out", "-"}
}

func TestAgentCreateRuntimeFlagsDefaultsAndCompletion(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	root := newRootCommand(&commandState{deps: deps})
	cmd, _, err := root.Find([]string{"create"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"runtime": "orka", "kagent-runtime": "go"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Fatalf("--%s default = %#v, want %q", name, flag, want)
		}
	}
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"__complete", "create", "--runtime", ""}, []string{"orka", "kagent", ":4"}},
		{[]string{"__complete", "create", "--kagent-runtime", ""}, []string{"go", "python", ":4"}},
	} {
		out.Reset()
		if err := execute(tc.args, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.wants {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("completion %v lacks %q: %s", tc.args, want, out.String())
			}
		}
	}
}

func TestAgentCreateExplicitOrkaMatchesDefaultBytes(t *testing.T) {
	run := func(extra ...string) (string, string) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		args := append(append([]string(nil), orkaCreateArgs()...), extra...)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		return out.String(), diagnostics.String()
	}
	implicitOut, implicitDiagnostics := run()
	explicitOut, explicitDiagnostics := run("--runtime", "orka")
	if implicitOut != explicitOut || implicitDiagnostics != explicitDiagnostics {
		t.Fatal("explicit --runtime orka changed existing output bytes or diagnostics")
	}
}

func TestCreateLiveKagentKeepsV0102Path(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute(kagentCreateArgs(), deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 1 || !strings.Contains(out.String(), "exact Kagent v0.10.2") {
		t.Fatalf("live path changed runtime/config routing: loads=%d output=%s", *loads, &out)
	}
	decoder := yaml.NewDecoder(&out)
	for _, kind := range []string{"Secret", "ModelConfig", "Agent"} {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		if doc["kind"] != kind {
			t.Fatalf("got %v, want %s", doc["kind"], kind)
		}
	}
	if !strings.Contains(diagnostics.String(), "not applied") {
		t.Fatal(diagnostics.String())
	}
}

func TestAgentCreateKagentRequiresNameBeforeConfigLoad(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"create", "--live", "--runtime", "kagent"}, deps)
	if err == nil || !strings.Contains(err.Error(), "requires") && !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing Kagent name error = %v", err)
	}
	if *loads != 0 {
		t.Fatalf("missing Kagent name loaded config %d times", *loads)
	}
}

func TestAgentCreateUnknownRuntimeFailsBeforeConfigLoad(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"create", "--live", "sample", "--runtime", "unknown"}, deps)
	if err == nil || !strings.Contains(err.Error(), "unsupported agent runtime") {
		t.Fatalf("unknown runtime error = %v", err)
	}
	if *loads != 0 {
		t.Fatalf("unknown runtime loaded config %d times", *loads)
	}
}

func TestAgentCreateRuntimeSpecificFlagRefusalsBeforeConfigLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"Kagent runtime with Orka", append(orkaCreateArgs(), "--kagent-runtime", "python"), "requires --runtime kagent"},
		{"explicit empty skills with Kagent", append(kagentCreateArgs(), "--skills="), "unsupported"},
		{"coordination with Kagent", append(kagentCreateArgs(), "--coordination"), "Orka-only"},
		{"allowed Agent with Kagent", append(kagentCreateArgs(), "--allowed-agent", "helper"), "Orka-only"},
		{"Azure deployment with Kagent", append(kagentCreateArgs(), "--azure-deployment="), "Orka-only"},
		{"Azure API version with Kagent", append(kagentCreateArgs(), "--azure-api-version="), "Orka-only"},
		{"Orka service with Kagent", append(kagentCreateArgs(), "--orka-api-service", "other"), "Orka-only"},
		{"result port with Kagent", append(kagentCreateArgs(), "--result-port", "1234"), "Orka-only"},
		{"rate flag with Kagent", append(kagentCreateArgs(), "--agent-requests-per-minute="), "unsupported"},
		{"schema flag with Kagent", append(kagentCreateArgs(), "--schema-target="), "unsupported"},
		{"result account flag with Kagent", append(kagentCreateArgs(), "--result-service-account="), "unsupported"},
		{"unknown declarative runtime", append(kagentCreateArgs(), "--kagent-runtime", "rust"), "go or python"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if *loads != 0 {
				t.Fatalf("flag refusal loaded config %d times", *loads)
			}
		})
	}
}
