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

// Catch eval accidentally becoming an evaluate alias, or loading cluster preferences.
func TestAgentEvalDiffRejectsLegacyReceiptOfflineCLI(t *testing.T) {
	receipt := `{"schemaVersion":2,"runID":"` + strings.Repeat("1", 32) + `","bundle":"sample","portableDigest":"` + strings.Repeat("a", 64) + `","casesDigest":"` + strings.Repeat("b", 64) + `","fullCaseSet":true,"gitCommit":"uncommitted","target":{"runtime":"orka","context":"context","namespace":"agents","clusterUID":"cluster-1","agent":"sample","agentUID":"agent-1","model":"test-model"},"result":"pass","cases":[{"id":"one","verdict":"pass","caseDigest":"` + strings.Repeat("c", 64) + `","inputDigest":"` + strings.Repeat("d", 64) + `","taskName":"task-one","taskUID":"task-1","answerSHA256":"` + strings.Repeat("e", 64) + `","assertions":[{"id":"text","type":"contains","definitionDigest":"` + strings.Repeat("f", 64) + `","verdict":"pass","reason":"assertion satisfied"}]}]}`
	before := filepath.Join(evalDiffTempDir(t), "before.json")
	after := filepath.Join(evalDiffTempDir(t), "after.json")
	if err := os.WriteFile(before, []byte(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, data, want string
		failure          bool
	}{
		{"old passing receipt", receipt, "", true},
		{"regression", strings.ReplaceAll(strings.ReplaceAll(receipt, `"pass"`, `"fail"`), "assertion satisfied", "assertion not satisfied"), "", true},
		{"definition changed", strings.Replace(receipt, `"definitionDigest":"`+strings.Repeat("f", 64), `"definitionDigest":"`+strings.Repeat("0", 64), 1), "", true},
		{"incompatible", strings.Replace(receipt, "test-model", "other-model", 1), "", true},
		{"invalid", `{"answer":"PRIVATE_PAYLOAD_CANARY"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(after, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			calls := 0
			deps.loadConfig = func(string, string) (*config.Config, error) { calls++; return nil, errors.New("PRIVATE_CONFIG_CANARY") }
			err := execute([]string{"eval", "diff", before, after}, deps)
			if calls != 0 || err == nil || err.Error() != "invalid evaluation receipt" || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("calls=%d err=%v out=%s diagnostics=%s", calls, err, out.String(), diagnostics.String())
			}
			if strings.Contains(out.String()+diagnostics.String()+fmtError(err), "PRIVATE_") {
				t.Fatal("private content exposed")
			}
		})
	}
}

func TestRootEvalOutputSelectionBeforeConfiguration(t *testing.T) {
	for _, format := range []string{"yaml", "", "JSON"} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute([]string{"eval", "--output=" + format}, deps)
		if err == nil || !strings.Contains(err.Error(), "eval output must be text or json") || *loads != 0 || out.Len() != 0 {
			t.Fatalf("invalid format reached configuration or stdout: err=%v loads=%d out=%s", err, *loads, &out)
		}
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			boundary := errors.New("configured boundary")
			deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, boundary }
			err := execute([]string{"eval", "--workspace", root, "--to-context", "kind-test", "--inference", "provider:inference", "-o", format}, deps)
			if !errors.Is(err, boundary) || *loads != 1 || out.Len() != 0 {
				t.Fatalf("selection rejected or contaminated stdout: %v loads=%d out=%s", err, *loads, &out)
			}
		})
	}
}

func TestRootEvalJSONRefusalIsOneSafeDocument(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, _ := suiteEvalSource(t)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.newApp = func(cfg *config.Config) *app.App {
		// Refuse at the configured evaluator boundary, without a runtime call.
		cfg.KubeContext = "different"
		return app.New(cfg)
	}
	err := execute([]string{"eval", "--workspace", root, "--to-context", "kind-test", "--inference", "provider:inference", "-o", "json"}, deps)
	var report struct {
		Status, Detail               string
		Executed, Total, NotExecuted int
	}
	if e := json.Unmarshal(out.Bytes(), &report); e != nil || err == nil || *loads != 1 || report.Status != "failed" || report.Executed != 0 || report.Total != 1 || report.NotExecuted != 1 {
		t.Fatalf("bad JSON refusal: %s err=%v decode=%v loads=%d", &out, err, e, *loads)
	}
	if !strings.Contains(report.Detail, "0 of 1 runs executed; 0 passed; 1 not executed") || strings.Contains(report.Detail, "aborted on execution error") {
		t.Fatal("preflight refusal misreported as completed run set or execution abort", report.Detail)
	}
}

func evalDiffTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestAgentEvalDiffArgumentsAndEvaluatePreserved(t *testing.T) {
	for _, args := range [][]string{{"eval", "diff"}, {"eval", "diff", "one"}, {"eval", "diff", "one", "two", "three"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "kmx eval diff") || *loads != 0 {
			t.Fatalf("%v loads=%d err=%v", args, *loads, err)
		}
	}
	for _, args := range [][]string{{"eval", "--help"}, {"eval", "diff", "--help"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("%v loads=%d err=%v", args, *loads, err)
		}
		if args[0] == "eval" && !strings.Contains(out.String(), "diff") {
			t.Fatal("diff missing from help")
		}
	}
}
