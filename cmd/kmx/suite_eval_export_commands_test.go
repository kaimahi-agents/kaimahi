package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuiteEvalExportRejectsArgumentsBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing member", []string{"eval", "export", "--out", "evidence.json"}, "member"},
		{"missing output", []string{"eval", "export", "--member", "greeter"}, "out"},
		{"blank member", []string{"eval", "export", "--member", "", "--out", "evidence.json"}, "non-empty --member"},
		{"blank output", []string{"eval", "export", "--member", "greeter", "--out", ""}, "non-empty --out"},
		{"blank workspace", []string{"eval", "export", "--member", "greeter", "--out", "evidence.json", "--workspace", ""}, "non-empty --workspace"},
		{"positional", []string{"eval", "export", "PRIVATE_EXPORT_CANARY"}, "no positional"},
		{"context", []string{"--context", "PRIVATE_EXPORT_CANARY", "eval", "export", "--member", "greeter", "--out", "evidence.json"}, "--context cannot"},
		{"destination", []string{"eval", "export", "--to", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --to"},
		{"inference", []string{"eval", "export", "--inference", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --inference"},
		{"engine", []string{"eval", "export", "--container-engine", "invalid", "--member", "greeter", "--out", "evidence.json"}, "container-engine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "state")
			t.Setenv("KMX_HOME", home)
			t.Chdir(t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 || out.Len() != 0 {
				t.Fatalf("error=%v loads=%d stdout=%q diagnostics=%q", err, *loads, out.String(), diagnostics.String())
			}
			if strings.Contains(fmtError(err)+out.String()+diagnostics.String(), "PRIVATE_EXPORT_CANARY") {
				t.Fatal("selector or positional input leaked")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("argument refusal touched state: %v", err)
			}
		})
	}
}

func TestSuiteEvalExportHelpAndCompletionAreInert(t *testing.T) {
	for _, args := range [][]string{{"eval", "export", "--help"}, {"__complete", "eval", "export", ""}, {"__complete", "eval", "export", "--member", ""}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "state")
			t.Setenv("KMX_HOME", home)
			t.Chdir(t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err != nil || *loads != 0 {
				t.Fatalf("error=%v loads=%d", err, *loads)
			}
			if args[len(args)-1] == "--help" {
				for _, word := range []string{"--member", "--out", "--workspace", "unsigned", "Go build", "modified"} {
					if !strings.Contains(out.String(), word) {
						t.Fatalf("missing export boundary %q", word)
					}
				}
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("help/completion touched state: %v", err)
			}
		})
	}
}

func TestSuiteEvalExportMissingProofIsOffline(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root, output := suiteEvalSource(t)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"eval", "export", "--workspace", root, "--member", "greeter", "--out", output}, deps)
	if err == nil || *loads != 0 || out.Len() != 0 || strings.Contains(err.Error(), "positional") || strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("offline consumer error=%v loads=%d stdout=%q", err, *loads, out.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("missing proof published evidence: %v", err)
	}
}
