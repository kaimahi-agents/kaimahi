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
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(parent, "passing.json")
}

func TestSuiteEvalOfflineBeforeConfiguration(t *testing.T) {
	for _, stage := range []string{"valid", "unsupported", "budget", "output", "selector", "empty workspace", "global context", "positional", "no workspace", "private source", "private descriptor", "bare"} {
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
				want = "required flag(s)"
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
			if stage == "valid" {
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

func TestSuiteEvalHelpersHelpAndCompletionRemainOffline(t *testing.T) {
	for _, args := range [][]string{{"eval", "--help"}, {"eval", "from-sessions", "--help"}, {"__complete", "eval", "--"}, {"eval", "from-sessions", "uid", "--sessions", "127.0.0.1:8080", "--assertions", "missing.yaml", "--out", "private.json"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute(args, deps)
		if *loads != 0 {
			t.Fatalf("helper loaded config: %v", args)
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
