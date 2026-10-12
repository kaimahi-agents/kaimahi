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

// Invalid mode selection must never reach configuration, let alone a target.
func TestWorkspaceEvaluateValidationBeforeConfiguration(t *testing.T) {
	valid := []string{"agent", "evaluate", "--workspace", "missing", "--to-context", "kind-proof", "--to-namespace", "orka-system", "--inference", "provider:inference", "--out", "receipt.json"}
	tests := [][]string{}
	for _, name := range []string{"workspace", "to-context", "to-namespace", "inference", "out"} {
		args := append([]string(nil), valid...)
		for i := range args {
			if args[i] == "--"+name {
				args[i+1] = ""
			}
		}
		tests = append(tests, args)
	}
	for _, flags := range [][]string{{"bundle"}, {"--context", "kind-other"}, {"--sessions", "127.0.0.1:1"}, {"--sessions-ca", "file"}, {"--from-sessions", "file"}, {"--source-sessions", "host:1"}, {"--source-sessions-ca", "file"}, {"--runtime", "orka"}, {"--model", "model"}, {"--bindings", "file"}, {"--override-gate"}, {"--require-evaluated"}, {"--case-timeout", "1s"}, {"--result-port", "0"}, {"--inference", "copilot"}, {"--to-namespace", "bad/name"}, {"--case", "bad\ncase"}, {"--case", "bad/name"}, {"--case", strings.Repeat("a", 64)}, {"--judge-key-env", "bad=name"}} {
		tests = append(tests, append(append([]string(nil), valid...), flags...))
	}
	for _, flags := range [][]string{{"--to-namespace", "orka-system"}, {"--inference", "provider:x"}, {"--out", "file"}} {
		tests = append(tests, append([]string{"agent", "evaluate", "bundle"}, flags...))
	}
	for i, args := range tests {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("KMX_HOME", state)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, errors.New("CONFIG_REACHED") }
			err := execute(args, deps)
			if err == nil || strings.Contains(err.Error(), "CONFIG_REACHED") || *loads != 0 {
				t.Fatalf("validation: %v loads=%d args=%q", err, *loads, args)
			}
			entries, e := os.ReadDir(state)
			if e != nil || len(entries) != 0 || out.Len() != 0 {
				t.Fatal("invalid mode performed work")
			}
		})
	}
}

func TestWorkspaceEvaluateEvidenceContextBoundsBeforeConfiguration(t *testing.T) {
	for name, context := range map[string]string{"unicode": "kind-café", "oversized": "kind-" + strings.Repeat("x", 2044)} {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, errors.New("CONFIG_REACHED") }
			err := execute([]string{"agent", "evaluate", "--workspace", "missing", "--to-context", context, "--to-namespace", "orka-system", "--inference", "provider:inference", "--out", "outside.json"}, deps)
			if err == nil || !strings.Contains(err.Error(), "invalid --to-context") || strings.Contains(err.Error(), context) || *loads != 0 {
				t.Fatalf("unserializable workspace context reached config: %v loads=%d", err, *loads)
			}
			// Narrow new-mode validation must not reject legacy bundle selectors.
			err = execute([]string{"agent", "evaluate", "bundle", "--to-context", context}, deps)
			if err == nil || err.Error() != "CONFIG_REACHED" || *loads != 1 {
				t.Fatalf("legacy context behavior changed: %v loads=%d", err, *loads)
			}
		})
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, errors.New("CONFIG_REACHED") }
	err := execute([]string{"agent", "evaluate", "--workspace", "missing", "--to-context", "kind-" + strings.Repeat("x", 2043), "--to-namespace", "orka-system", "--inference", "provider:inference", "--out", "outside.json"}, deps)
	if err == nil || err.Error() != "CONFIG_REACHED" || *loads != 1 {
		t.Fatalf("valid maximum-length context rejected: %v loads=%d", err, *loads)
	}
}

func TestWorkspaceEvaluateHelpExplainsNativeAndLegacyContracts(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"agent", "evaluate", "--help"}, deps); err != nil || *loads != 0 {
		t.Fatalf("help reached operations: %v", err)
	}
	for _, phrase := range []string{"evaluate [<bundle-dir>]", "exact prepared", "read-only Provider", "--to-namespace", "--inference", "--out", "diagnostic", "--show-content", "requiredImages: []", "release lift is not available", "bundle's current portable digest", "Answers are printed", "--from-sessions"} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("help omits %q", phrase)
		}
	}
}

func TestWorkspaceEvaluateExplicitContextAndStaticHelp(t *testing.T) {
	for _, args := range [][]string{{"agent", "evaluate", "--help"}, {"__complete", "agent", "evaluate", "--workspace", ""}, {"__complete", "agent", "evaluate", "--inference", ""}, {"__complete", "agent", "evaluate", "--to-context", ""}, {"__complete", "agent", "evaluate", "--to-namespace", ""}, {"__complete", "agent", "evaluate", "--out", ""}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if e := execute(args, deps); e != nil || *loads != 0 {
			t.Fatalf("static: %v loads=%d", e, *loads)
		}
		if args[0] == "__complete" && !strings.Contains(out.String(), ":4") {
			t.Fatalf("workspace selector completion is not static/no-file: %q", out.String())
		}
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.loadConfig = func(context, engine string) (*config.Config, error) {
		*loads++
		if context != "kind-proof" {
			t.Errorf("configured wrong context %q", context)
		}
		return nil, errors.New("EXPECTED_CONFIG_BOUNDARY")
	}
	e := execute([]string{"agent", "evaluate", "--workspace", "missing", "--to-context", "kind-proof", "--to-namespace", "orka-system", "--inference", "provider:inference", "--out", filepath.Join(t.TempDir(), "out.json"), "--show-content"}, deps)
	if e == nil || e.Error() != "EXPECTED_CONFIG_BOUNDARY" || *loads != 1 {
		t.Fatalf("valid mode did not reach selected config: %v %d", e, *loads)
	}
}
