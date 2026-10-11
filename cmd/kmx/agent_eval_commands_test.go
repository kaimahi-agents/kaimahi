package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Catch eval accidentally becoming an evaluate alias, or loading cluster preferences.
func TestAgentEvalDiffOfflineCLI(t *testing.T) {
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
		{"stable", receipt, "stable", false},
		{"regression", strings.ReplaceAll(strings.ReplaceAll(receipt, `"pass"`, `"fail"`), "assertion satisfied", "assertion not satisfied"), "regression", true},
		{"definition changed", strings.Replace(receipt, `"definitionDigest":"`+strings.Repeat("f", 64), `"definitionDigest":"`+strings.Repeat("0", 64), 1), "definitionChanged", true},
		{"incompatible", strings.Replace(receipt, "test-model", "other-model", 1), "incompatible", true},
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
			err := execute([]string{"agent", "eval", "diff", before, after}, deps)
			if calls != 0 || (err != nil) != tc.failure || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("calls=%d err=%v out=%s diagnostics=%s", calls, err, out.String(), diagnostics.String())
			}
			if strings.Contains(out.String()+diagnostics.String()+fmtError(err), "PRIVATE_") {
				t.Fatal("private content exposed")
			}
		})
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
	for _, args := range [][]string{{"agent", "eval", "diff"}, {"agent", "eval", "diff", "one"}, {"agent", "eval", "diff", "one", "two", "three"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), "kmx agent eval diff") || *loads != 0 {
			t.Fatalf("%v loads=%d err=%v", args, *loads, err)
		}
	}
	for _, args := range [][]string{{"agent", "eval", "--help"}, {"agent", "eval", "diff", "--help"}, {"agent", "evaluate", "--help"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("%v loads=%d err=%v", args, *loads, err)
		}
		if args[1] == "evaluate" && !strings.Contains(out.String(), "--case-timeout") {
			t.Fatal("existing evaluate command changed")
		}
		if args[1] == "eval" && !strings.Contains(out.String(), "diff") {
			t.Fatal("diff missing from help")
		}
	}
}
