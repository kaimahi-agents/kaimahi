package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func suiteEvalSource(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("Private instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "authored-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	parent := privateTempDir(t)
	return root, filepath.Join(parent, "passing.json")
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSuiteEvalOfflineBeforeConfiguration(t *testing.T) {
	for _, stage := range []string{"valid", "managed", "unsupported", "budget", "output", "selector", "empty workspace", "global context", "positional", "no workspace", "private source", "private descriptor", "bare"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, outfile := suiteEvalSource(t)
			nested := filepath.Join(root, "nested")
			if err := os.Mkdir(nested, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(nested)
			args := []string{"eval", "--to-context", "kind-target", "--inference", "provider:inference", "--out", outfile}
			want := "configured boundary"
			switch stage {
			case "managed":
				args = args[:len(args)-2]
			case "unsupported":
				if err := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte("id: unsupported\ninput: private\nassertions: [{id: tool, type: toolCalled, tool: private}]\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "unsupported native suite evaluator"
			case "budget":
				// One huge exact model reserves receipt metadata before any target selection.
				member := filepath.Join(root, "agents/greeter/member.yaml")
				raw, err := os.ReadFile(member)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.ReplaceAll(raw, []byte("authored-model"), []byte(strings.Repeat("m", 950000)))
				if err := os.WriteFile(member, raw, 0600); err != nil {
					t.Fatal(err)
				}
				want = "projection budget"
			case "output":
				args[len(args)-1] = filepath.Join(root, "forbidden.json")
				want = "private"
			case "selector":
				args[4] = "model:private"
				want = "native evaluation requires"
			case "empty workspace":
				args = append(args, "--workspace", "")
				want = "non-empty --workspace"
			case "global context":
				args = append(args, "--context", "other")
				want = "--context cannot"
			case "positional":
				args = append(args, "registry.example/suite@sha256:abc")
				want = "accepts no positional"
			case "no workspace":
				if err := os.Remove(filepath.Join(root, "workspace.yaml")); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--workspace", root)
				want = "pass --workspace"
			case "private source":
				if err := os.WriteFile(filepath.Join(root, "agents/greeter/member.yaml"), []byte("PRIVATE_PARSE_CANARY: malformed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "refused before native changes"
			case "private descriptor":
				raw, err := os.ReadFile(filepath.Join(root, "workspace.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.ReplaceAll(raw, []byte("agents/greeter/"), []byte("agents/PRIVATE_PARSE_CANARY/"))
				if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				want = "refused before native changes"
			case "bare":
				args = []string{"eval"}
				want = "native evaluation requires"
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			created := 0
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != "kind-target" {
					t.Fatalf("configured wrong explicit context %q", ctx)
				}
				return nil, errors.New("configured boundary")
			}
			deps.newApp = func(*config.Config) *app.App { created++; t.Fatal("unexpected App construction"); return nil }
			err := execute(args, deps)
			wantLoads := 0
			if stage == "valid" || stage == "managed" {
				wantLoads = 1
			}
			if err == nil || !strings.Contains(err.Error(), want) || *loads != wantLoads || created != 0 {
				t.Fatalf("stage=%s err=%v loads=%d App=%d", stage, err, *loads, created)
			}
			if strings.Contains(fmtError(err)+out.String()+diagnostics.String(), "PRIVATE_PARSE_CANARY") {
				t.Fatal("source diagnostics escaped")
			}
			if stage == "no workspace" && !errors.Is(err, workspace.ErrNotFound) {
				t.Fatalf("missing workspace identity lost: %v", err)
			}
			if _, err := os.Stat(outfile); !os.IsNotExist(err) {
				t.Fatal("preparation exported receipt")
			}
		})
	}
}

func TestSuiteEvalConfiguredTargetAndRetryIdentity(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, outfile := suiteEvalSource(t)
	outfile = filepath.Join(filepath.Dir(outfile), "owner's passing.json")
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	var configured *app.App
	deps.newApp = func(cfg *config.Config) *app.App {
		if cfg.KubeContext != "kind-target" {
			t.Fatalf("wrong requested context: %q", cfg.KubeContext)
		}
		configured = app.New(cfg)
		cfg.KubeContext = "other" // force App's target mismatch, before any tool call
		return configured
	}
	args := []string{"eval", "--workspace", root, "--to-context", "kind-target", "--inference", "provider:inference", "--out", outfile}
	err := execute(args, deps)
	if err == nil || !strings.Contains(err.Error(), "refused before native changes") || configured == nil || configured.Cfg.ContextSource != config.SourceFlag || strings.Contains(configured.InvocationCommand, "kmx --context") || !strings.Contains(configured.InvocationCommand, "--to-context kind-target") || !strings.Contains(configured.InvocationCommand, "owner'\"'\"'s passing.json'") || !strings.Contains(configured.InvocationCommand, root) {
		t.Fatalf("configured identity: %v %+v", err, configured)
	}
}

func suiteKagentCLIArgs(t *testing.T, extension string) ([]string, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("PRIVATE_CLI_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"create", "greeter", "--workspace", root, "--runtime", "kagent", "--model", "fixture-suite", "--instructions", input}, deps); err != nil || *loads != 0 {
		t.Fatalf("offline kagent authoring: %v loads=%d", err, *loads)
	}
	if err := os.WriteFile(filepath.Join(root, "eval/second"+extension), []byte("id: second\ninput: private\nassertions: [{id: text, type: contains, value: answer}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	binding := agentruntime.KagentBindings{APIVersion: agentruntime.KagentBindingsAPIVersion, Kind: agentruntime.KagentBindingsKind, Namespace: "agents", ModelConfig: agentruntime.KagentModelConfigBindings{Provider: "openai", SecretRef: agentruntime.KagentSecretRefBindings{Name: "model-key", Key: "api-key"}}}
	raw, err := agentruntime.EncodeKagentBindings(binding)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(private, "bindings"+extension)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"eval", "--workspace", root, "--to-context", "kind-target", "--bindings", file, "--out", filepath.Join(private, "passing.json")}, root, file
}

func TestSuiteKagentEvalOfflineConfigurationBoundary(t *testing.T) {
	for _, extension := range []string{".yaml", ".yml"} {
		t.Run(extension, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			args, _, _ := suiteKagentCLIArgs(t, extension)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			boundary := errors.New("kagent configured boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != "kind-target" {
					t.Fatalf("wrong context %q", ctx)
				}
				return nil, boundary
			}
			deps.newApp = func(*config.Config) *app.App { t.Fatal("constructed Orka/configured App"); return nil }
			err := execute(args, deps)
			if !errors.Is(err, boundary) || *loads != 1 {
				t.Fatalf("offline preparation did not reach intended boundary: %v loads=%d", err, *loads)
			}
			if _, err := os.Stat(args[len(args)-1]); !os.IsNotExist(err) {
				t.Fatal("preparation exported passing evidence")
			}
		})
	}
}

func TestSuiteKagentEvalRefusesBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"both selectors", "exactly one inference or bindings selector"},
		{"no selector", "exactly one inference or bindings selector"},
		{"Orka selector", "native kagent evaluation requires --bindings"},
		{"namespace", "native kagent namespace must agree with bindings"},
		{"result port", "native kagent evaluation requires --bindings and refuses --result-port"},
		{"malformed binding", "native kagent requires bounded confined author-owned bindings"},
		{"tool assertion", "unsupported native suite evaluator"},
		{"judge assertion", "llmJudge assertions require --judge-base-url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			args, root, binding := suiteKagentCLIArgs(t, ".yml")
			switch tc.name {
			case "both selectors":
				args = append(args, "--inference", "provider:inference")
			case "no selector":
				args = append(args[:5], args[7:]...)
			case "Orka selector":
				args[5], args[6] = "--inference", "provider:inference"
			case "namespace":
				args = append(args, "--to-namespace", "other")
			case "result port":
				args = append(args, "--result-port", "19180")
			case "malformed binding":
				if err := os.WriteFile(binding, []byte("PRIVATE_CLI_CANARY: malformed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "tool assertion", "judge assertion":
				assertion := "id: tool, type: toolCalled, tool: private"
				if tc.name == "judge assertion" {
					assertion = "id: judge, type: llmJudge, rubric: private, minScore: 50, judgeModel: pinned-model, judgeVersion: v1"
				}
				if err := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte("id: unsupported\ninput: private\nassertions: [{"+assertion+"}]\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.newApp = func(*config.Config) *app.App { t.Fatal("refusal reached App"); return nil }
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 {
				t.Fatalf("intended refusal %q: %v loads=%d", tc.want, err, *loads)
			}
			if strings.Contains(fmtError(err)+out.String()+diagnostics.String(), "PRIVATE_CLI_CANARY") {
				t.Fatal("private source escaped")
			}
		})
	}
}

func TestSuiteEvalHelpersHelpAndCompletionRemainOffline(t *testing.T) {
	t.Chdir(privateTempDir(t))
	for _, args := range [][]string{{"eval", "--help"}, {"eval", "diff", "--help"}, {"eval", "from-sessions", "--help"}, {"__complete", "eval", "--"}, {"__complete", "eval", "diff", ""}, {"eval", "from-sessions", "uid", "--sessions", "127.0.0.1:8080", "--assertions", "missing.yaml", "--out", "private.json"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if *loads != 0 {
			t.Fatalf("helper loaded config: %v", args)
		}
		if len(args) == 2 && args[0] == "eval" && args[1] == "--help" {
			for _, text := range []string{"--bindings", "--inference", "unsecure", "trusted-proxy", "--result-port"} {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("missing runtime boundary in help: %q", text)
				}
			}
		}
		if args[0] != "__complete" && args[len(args)-1] == "private.json" {
			if err == nil || !strings.Contains(err.Error(), "assertion") {
				t.Fatalf("helper did not reach its offline consumer: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRootPushEvaluationRoutesToPrivateFileConsumer(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, _ := suiteEvalSource(t)
	for _, stage := range []string{"malformed", "empty", "extracted"} {
		t.Run(stage, func(t *testing.T) {
			parent := t.TempDir()
			if err := os.Chmod(parent, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(parent, "receipt.json")
			if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"push", "--workspace", root, "--evaluation", file, "--to-layout", filepath.Join(t.TempDir(), "layout"), "suite:v1"}
			want := "invalid native passing receipt"
			if stage == "empty" {
				args[4] = ""
				want = "non-empty --evaluation"
			}
			if stage == "extracted" {
				args = []string{"suite", "push", root, "suite:v1", "--evaluation", file}
				want = "unknown flag"
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), want) || *loads != 0 {
				t.Fatalf("receipt route: %v loads=%d", err, *loads)
			}
		})
	}
}
