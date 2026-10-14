package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
)

func suiteJudgeCase(t *testing.T, root string) {
	t.Helper()
	raw := "id: rubric\ninput: PRIVATE_JUDGE_INPUT\nassertions: [{id: judge, type: llmJudge, rubric: PRIVATE_RUBRIC, minScore: 70, judgeModel: judge-model, judgeVersion: v1}]\n"
	if err := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

// Removing offline judge selection or admitting unsupported tools before it must
// fail here, rather than reach cluster configuration with unusable evidence.
func TestSuiteJudgeSelectionBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
		loads int
	}{
		{"missing endpoint", nil, "llmJudge assertions require --judge-base-url", 0},
		{"valid endpoint", []string{"--judge-base-url", "https://judge.example.invalid"}, "configured boundary", 1},
		{"named endpoint", []string{"--to", "dev", "--judge-base-url", "https://judge.example.invalid"}, "configured boundary", 1},
		{"invalid credential selection", []string{"--judge-base-url", "https://judge.example.invalid", "--judge-key-env", "bad-name"}, "valid environment variable name", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, outfile := suiteEvalSource(t)
			suiteJudgeCase(t, root)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(ctx, _ string) (*config.Config, error) {
				*loads++
				if ctx != "kind-target" {
					t.Fatalf("unresolved judge destination: %q", ctx)
				}
				return nil, errors.New("configured boundary")
			}
			args := []string{"eval", "--workspace", root, "--out", outfile}
			if tc.name == "named endpoint" {
				if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("version: 1\ntargets: {dev: {context: kind-target, runtime: orka, inference: 'provider:inference'}}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				args = append(args, "--to-context", "kind-target", "--inference", "provider:inference")
			}
			err := execute(append(args, tc.flags...), deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != tc.loads {
				t.Fatalf("selection: %v loads=%d", err, *loads)
			}
			if _, err := os.Stat(outfile); !os.IsNotExist(err) {
				t.Fatal("preflight exported evidence")
			}
		})
	}
}

// Both argv spellings must omit the endpoint from the command that the real
// noninteractive remote guard includes in its refusal and retry advice.
func TestSuiteJudgeEndpointNeverInGuardRecovery(t *testing.T) {
	const endpoint = "https://judge.example.invalid/PRIVATE_ENDPOINT_CANARY"
	for i, flags := range [][]string{{"--judge-base-url", endpoint}, {"--judge-base-url=" + endpoint}} {
		t.Run([]string{"split", "equals"}[i], func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, outfile := suiteEvalSource(t)
			suiteJudgeCase(t, root)
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			var configured *app.App
			deps.newApp = func(cfg *config.Config) *app.App {
				configured = app.New(cfg)
				cfg.KubeContext = "different"
				return configured
			}
			args := []string{"eval", "--workspace", root, "--to-context", "kind-target", "--inference", "provider:inference", "--out", outfile}
			if err := execute(append(args, flags...), deps); err == nil || configured == nil {
				t.Fatalf("did not reach configured boundary: %v", err)
			}
			kube, err := guard.ParseKubeconfig([]byte(`{"current-context":"kind-target","clusters":[{"name":"remote","cluster":{"server":"https://remote.example.invalid"}}],"contexts":[{"name":"kind-target","context":{"cluster":"remote"}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			refusal := guard.CheckContext(t.Context(), kube, guard.Request{Action: "evaluate suite", Context: "kind-target", Source: config.SourceFlag, Command: configured.InvocationCommand}, &diagnostics, nil)
			if refusal == nil || !strings.Contains(refusal.Error(), "KAIMAHI_CONFIRM=kind-target") {
				t.Fatalf("not a named guard refusal: %v", refusal)
			}
			text := configured.InvocationCommand + refusal.Error() + diagnostics.String() + out.String()
			if strings.Contains(text, "PRIVATE_ENDPOINT_CANARY") || strings.Contains(text, "--judge-base-url") {
				t.Fatalf("judge endpoint leaked into guard recovery: %s", text)
			}
			if !strings.Contains(configured.InvocationCommand, "--to-context kind-target") {
				t.Fatal("safe selectors lost")
			}
		})
	}
}
