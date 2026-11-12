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
