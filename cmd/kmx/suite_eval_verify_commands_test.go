package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
)

func TestSuiteEvalVerifyArgumentsBeforeConfiguration(t *testing.T) {
	base := []string{"eval", "verify", "--suite", "layout", "--evidence", "evidence.json", "--expect", "expected.json"}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"suite required", []string{"eval", "verify", "--evidence", "evidence.json", "--expect", "expected.json"}, "suite"},
		{"evidence required", []string{"eval", "verify", "--suite", "layout", "--expect", "expected.json"}, "evidence"},
		{"expect required", []string{"eval", "verify", "--suite", "layout", "--evidence", "evidence.json"}, "expect"},
		{"suite blank", append(append([]string{}, base...), "--suite", ""), "non-empty --suite"},
		{"evidence blank", append(append([]string{}, base...), "--evidence", ""), "non-empty --evidence"},
		{"expect blank", append(append([]string{}, base...), "--expect", ""), "non-empty --expect"},
		{"layout blank", append(append([]string{}, base...), "--from-layout", ""), "non-empty --from-layout"},
		{"output blank", append(append([]string{}, base...), "--output", ""), "output"},
		{"output unsupported", append(append([]string{}, base...), "--output", "PRIVATE_VERIFY_CANARY"), "output"},
		{"context", append([]string{"--context", "PRIVATE_VERIFY_CANARY"}, base...), "--context cannot"},
		{"engine", append(append([]string{}, base...), "--container-engine", "docker"), "container-engine"},
		{"positional", append(append([]string{}, base...), "PRIVATE_VERIFY_CANARY"), "no positional"},
		{"workspace", append(append([]string{}, base...), "--workspace", "PRIVATE_VERIFY_CANARY"), "unknown flag: --workspace"},
		{"native inference", append(append([]string{}, base...), "--inference", "PRIVATE_VERIFY_CANARY"), "unknown flag: --inference"},
		{"artifact output", append(append([]string{}, base...), "--out", "PRIVATE_VERIFY_CANARY"), "unknown flag: --out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "state")
			t.Setenv("KMX_HOME", home)
			t.Chdir(t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 || out.Len() != 0 {
				t.Fatalf("error=%v loads=%d stdout=%q", err, *loads, out.String())
			}
			if strings.Contains(fmtError(err)+out.String()+diagnostics.String(), "PRIVATE_VERIFY_CANARY") {
				t.Fatal("unverified input leaked")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("verification arguments touched state: %v", err)
			}
		})
	}
}

func TestSuiteEvalVerifyHelpCompletionInert(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "verify", "--help"},
		{"__complete", "eval", "verify", ""},
		{"__complete", "eval", "verify", "--suite", ""},
		{"__complete", "eval", "verify", "--output", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "absent-state")
			t.Setenv("KMX_HOME", home)
			t.Chdir(t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if err != nil || *loads != 0 {
				t.Fatalf("error=%v loads=%d", err, *loads)
			}
			if args[len(args)-1] == "--help" {
				for _, word := range []string{"--suite", "--from-layout", "--evidence", "--expect", "unsigned", "offline", "json"} {
					if !strings.Contains(out.String(), word) {
						t.Errorf("help missing %q", word)
					}
				}
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("help/completion touched state: %v", err)
			}
		})
	}
}

func TestSuiteEvalVerifyMissingInputsTypedAndOffline(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "absent-state")
	t.Setenv("KMX_HOME", home)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"eval", "verify", "--suite", filepath.Join(root, "layout"), "--evidence", filepath.Join(root, "PRIVATE_VERIFY_CANARY"), "--expect", filepath.Join(root, "PRIVATE_VERIFY_CANARY")}, deps)
	var refusal *agentsuite.EvidenceRefusal
	if !errors.As(err, &refusal) || refusal.Code != "missing" || !strings.Contains(err.Error(), "evaluation: not verified [missing]") || *loads != 0 || out.Len() != 0 {
		t.Fatalf("error=%v loads=%d stdout=%q", err, *loads, out.String())
	}
	if strings.Contains(fmtError(err)+out.String()+diagnostics.String(), "PRIVATE_VERIFY_CANARY") {
		t.Fatal("private path leaked")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("offline verification touched state: %v", err)
	}
}
