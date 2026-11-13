package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localagent"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestResidentCLIResolvesExactLocalAndHostedRoutes(t *testing.T) {
	file := cliPreviewFile(t)
	for _, provider := range []string{"ollama", "foundry-local", "openai", "anthropic", "compatible"} {
		t.Run(provider, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.residentAgents = true
			deps.discoverModels = func(context.Context) []modelaccess.Report {
				return []modelaccess.Report{{State: modelaccess.Installed, Models: []modelaccess.Model{{Route: modelaccess.Route{Provider: provider, Model: "unit", Endpoint: "http://127.0.0.1:1234/v1", Auth: "none"}}}}}
			}
			called := false
			deps.activateLocalAgent = func(_ context.Context, _ string, _ *agentfile.Pair, p, endpoint, model string, opt localagent.ActivationOptions) (localagent.Instance, bool, error) {
				called = true
				if p != provider || model != "unit" || endpoint == "" {
					t.Fatalf("route %s %s %s", p, endpoint, model)
				}
				if provider == "openai" && opt.KeyEnv != "OPENAI_API_KEY" || provider == "anthropic" && opt.KeyEnv != "ANTHROPIC_API_KEY" {
					t.Fatal("wrong credential reference")
				}
				return localagent.Instance{Name: "unit"}, true, nil
			}
			args := []string{"run", file, "--provider", provider, "--model", "unit"}
			if provider == "compatible" {
				args = append(args, "--provider-url", "https://models.example/v1")
			}
			if err := execute(args, deps); err != nil || !called || *loads != 0 {
				t.Fatalf("called=%v loads=%d err=%v", called, *loads, err)
			}
		})
	}
}

func TestModelMatchReportsUnknownWithoutConfiguration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "model.json")
	raw, _ := json.Marshal(modelaccess.Profile{Route: modelaccess.Route{Provider: "openai", Model: "unit", Endpoint: "https://api.openai.com/v1", Auth: "env", KeyEnv: "OPENAI_API_KEY"}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	err = execute([]string{"model", "match", path, "--require-reasoning"}, deps)
	if err == nil || *loads != 0 || !strings.Contains(out.String(), `"status":"unknown"`) {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestResidentSelectorRefusalsKeepLiveRunFailureExit(t *testing.T) {
	file := cliPreviewFile(t)
	for _, flags := range [][]string{
		{"--require-context", "1"}, {"--require-context", "-1"}, {"--require-output", "1"},
		{"--require-reasoning"}, {"--model-profile", "missing.json"}, {"--provider-key-env", "KEY"},
		{"--upgrade"}, {"--new"}, {"--name", "worker"}, {"--yes"},
	} {
		for _, mode := range []string{"live", "once"} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.residentAgents = true
			args := []string{"run", "--agent", "live", "--prompt", "hi"}
			want := 1
			if mode == "once" {
				args = []string{"run", file, "--once"}
				want = 2
			}
			err := execute(append(args, flags...), deps)
			if err == nil || exitCode(err) != want || *loads != 0 || out.Len() != 0 {
				t.Fatalf("%s %v exit=%d loads=%d err=%v", mode, flags, exitCode(err), *loads, err)
			}
		}
	}
}

func TestOllamaExactReuseUsesFrozenMetadataWithoutCredential(t *testing.T) {
	file := cliPreviewFile(t)
	pair, err := agentfile.LoadPair(file, "")
	if err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.residentAgents = true
	yes := true
	record := localagent.Instance{ID: localagent.ID(pair.AgentPath), SnapshotRevision: pair.SnapshotRevision, State: "running", Provider: "ollama", Model: "unit", Endpoint: "https://unavailable.invalid/v1", KeyEnv: "MISSING_KEY", Capabilities: modelaccess.Capabilities{Reasoning: &yes}, Requirements: modelaccess.Requirements{Reasoning: true}}
	deps.readLocalAgents = func(bool) ([]localagent.Instance, error) { return []localagent.Instance{record}, nil }
	called := false
	deps.activateLocalAgent = func(_ context.Context, _ string, _ *agentfile.Pair, _, _, _ string, opt localagent.ActivationOptions) (localagent.Instance, bool, error) {
		called = true
		if opt.Capabilities.Reasoning == nil || !*opt.Capabilities.Reasoning {
			t.Fatal("lost frozen metadata")
		}
		return record, false, nil
	}
	err = execute([]string{"run", file, "--provider", "ollama", "--provider-url", record.Endpoint, "--model", "unit", "--provider-key-env", "MISSING_KEY", "--require-reasoning"}, deps)
	if err != nil || !called || *loads != 0 {
		t.Fatalf("reuse reached metadata/credential access: %v", err)
	}
}

func TestRunAndMatchAcceptRelativeModelProfile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := os.WriteFile("helper.agent.md", []byte("---\nname: helper\ntools: []\n---\nBe helpful.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("model.json", []byte(`{"route":{"provider":"fake","model":"fake","auth":"none"},"capabilities":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"model.json", "./model.json"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.residentAgents = true
		called := false
		deps.activateLocalAgent = func(context.Context, string, *agentfile.Pair, string, string, string, localagent.ActivationOptions) (localagent.Instance, bool, error) {
			called = true
			return localagent.Instance{Name: "helper"}, true, nil
		}
		if err := execute([]string{"model", "match", path}, deps); err != nil {
			t.Fatal(err)
		}
		if err := execute([]string{"run", "helper.agent.md", "--model-profile", path}, deps); err != nil || !called || *loads != 0 {
			t.Fatalf("run relative profile: %v", err)
		}
	}
}
