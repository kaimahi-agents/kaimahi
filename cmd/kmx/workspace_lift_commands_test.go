package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

const composedDestinationConfig = `version: 1
evalTarget: unrelated
publishRepository: registry.example/agents
targets:
  dev: {context: kind-dev, namespace: dev-agents, runtime: orka, inference: 'provider:dev'}
  prod: {context: aks-prod, namespace: prod-agents, runtime: orka, inference: 'provider:prod', evaluationTarget: dev}
  unrelated: {context: wrong, inference: 'provider:wrong'}
`

func TestWorkspaceLiftSelectsNamedDestinationBeforeConfig(t *testing.T) {
	for _, mode := range []string{"default", "explicit context", "changed config"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			t.Chdir(root)
			path := filepath.Join(root, "kmx.yaml")
			if err := os.WriteFile(path, []byte(composedDestinationConfig), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"lift", "--to", "prod"}
			wantContext := "aks-prod"
			if mode == "explicit context" {
				args = append(args, "--to-context", "explicit")
				wantContext = "explicit"
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("cold workspace Lift config boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != wantContext {
					t.Fatalf("context=%q want %q", ctx, wantContext)
				}
				if mode == "changed config" {
					if err := os.WriteFile(path, []byte("version: 1\ntargets: {prod: {context: redirected}}\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil, boundary
			}
			err := execute(args, deps)
			if !errors.Is(err, boundary) || *loads != 1 {
				t.Fatalf("err=%v loads=%d diag=%s", err, *loads, &diag)
			}
			if strings.Contains(diag.String(), "redirected") {
				t.Fatal("config was reread")
			}
		})
	}
}

func TestWorkspaceLiftRejectsIncompleteOrUnsupportedPlanOffline(t *testing.T) {
	for _, scenario := range []string{"no to", "no config", "no evaluationTarget", "no repository", "kagent", "missing inference", "evidence", "from-layout", "bindings", "explicit empty", "extra argument"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			t.Chdir(root)
			raw := composedDestinationConfig
			args := []string{"lift", "--to", "prod", "--plan"}
			switch scenario {
			case "no to":
				args = []string{"lift", "--plan"}
			case "no config":
				raw = ""
			case "no evaluationTarget":
				raw = strings.Replace(raw, ", evaluationTarget: dev", "", 1)
			case "no repository":
				raw = strings.Replace(raw, "publishRepository: registry.example/agents\n", "", 1)
			case "kagent":
				raw = strings.ReplaceAll(raw, "runtime: orka", "runtime: kagent")
				raw = strings.ReplaceAll(raw, "inference:", "bindings:")
			case "missing inference":
				raw = strings.Replace(raw, ", inference: 'provider:dev'", "", 1)
			case "evidence":
				args = append(args, "--evidence", "private.json")
			case "from-layout":
				args = append(args, "--from-layout", ".")
			case "bindings":
				args = append(args, "--bindings", "bindings.yaml")
			case "explicit empty":
				args = append(args, "--to-context", "")
			case "extra argument":
				args = append(args, "one", "two")
			}
			if raw != "" {
				if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			if err := execute(args, deps); err == nil || *loads != 0 {
				t.Fatalf("scenario=%s err=%v loads=%d", scenario, err, *loads)
			}
		})
	}
}

func TestWorkspaceLiftConcreteSelectionIsFrozenAndOverridesDestinationOnly(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "overrides"}[explicit], func(t *testing.T) {
			root, _ := suiteEvalSource(t)
			t.Chdir(root)
			path := filepath.Join(root, "kmx.yaml")
			raw := composedDestinationConfig + "registries:\n  registry.example: {plainHTTP: true}\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			deps, _ := testDependencies(&out, &out)
			cmd := newReleaseLiftCommand(&commandState{deps: deps})
			destination := app.ReleaseLiftOptions{}
			if explicit {
				if err := cmd.ParseFlags([]string{"--to-context", "explicit-prod", "--to-namespace", "explicit-ns", "--inference", "provider:explicit", "--plain-http=false"}); err != nil {
					t.Fatal(err)
				}
				destination = app.ReleaseLiftOptions{ToContext: "explicit-prod", ToNamespace: "explicit-ns", Inference: "provider:explicit"}
			}
			opt, err := resolveWorkspaceLiftOptions(cmd, "prod", destination, app.SuiteWorkspaceEvaluationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("malformed: [\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if opt.EvalName != "dev" || opt.Evaluation.ToContext != "kind-dev" || opt.Evaluation.ToNamespace != "dev-agents" || opt.Evaluation.Inference != "provider:dev" || opt.Repository != "registry.example/agents" {
				t.Fatalf("wrong evaluation selection: %+v", opt)
			}
			wantContext, wantNS, wantInference := "aks-prod", "prod-agents", "provider:prod"
			if explicit {
				wantContext, wantNS, wantInference = "explicit-prod", "explicit-ns", "provider:explicit"
			}
			if opt.Destination.ToContext != wantContext || opt.Destination.ToNamespace != wantNS || opt.Destination.Inference != wantInference || opt.Destination.PlainHTTP == explicit {
				t.Fatalf("wrong destination overrides: %+v", opt.Destination)
			}
		})
	}
}

func TestWorkspaceLiftColdPlanNeedsNoOperationalConfigOrProof(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			t.Chdir(root)
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(composedDestinationConfig), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			args := []string{"lift", "--to", "prod", "--plan"}
			if jsonOutput {
				args = append(args, "-o", "json")
			}
			if err := execute(args, deps); err != nil {
				t.Fatalf("cold plan: %v", err)
			}
			if *loads != 0 {
				t.Fatalf("plan loaded operational config %d times", *loads)
			}
			if jsonOutput {
				var result map[string]any
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatalf("not one JSON result: %s (%v)", &out, err)
				}
			} else if out.Len() != 0 || len(strings.Split(strings.TrimSpace(diag.String()), "\n")) != 4 {
				t.Fatalf("not four stderr lines: stdout=%s stderr=%s", &out, &diag)
			}
			if strings.Contains(diag.String(), "Type ") {
				t.Fatal("read-only plan prompted")
			}
		})
	}
}

func TestWorkspaceLiftDoesNotWeakenDigestEvidenceRequirement(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	err := execute([]string{"lift", "registry.example/agents@sha256:" + strings.Repeat("a", 64), "--to-context", "aks-prod", "--inference", "provider:prod"}, deps)
	if !errors.Is(err, app.ErrReleaseLift) || !strings.Contains(err.Error(), "--evidence") || *loads != 0 {
		t.Fatalf("digest Lift without evidence lost typed/actionable refusal: %v loads=%d", err, *loads)
	}
}
