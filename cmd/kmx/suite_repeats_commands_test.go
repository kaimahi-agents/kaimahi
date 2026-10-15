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

func TestSuiteRepeatPolicyBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		policy string
		loads  int
	}{{"runs: 3\nminPasses: 2\n", 1}, {"runs: 3\nminPasses: 4\n", 0}, {"runs: null\n", 0}} {
		t.Run(tc.policy, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, outfile := suiteEvalSource(t)
			if err := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte("id: first\ninput: question\nexpectContains: [answer]\n"+tc.policy), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.loadConfig = func(_, _ string) (*config.Config, error) { *loads++; return nil, errors.New("configured boundary") }
			err := execute([]string{"eval", "--workspace", root, "--to-context", "kind-target", "--inference", "provider:inference", "--out", outfile}, deps)
			if err == nil || *loads != tc.loads {
				t.Fatalf("policy preparation %v loads%d", err, *loads)
			}
			if tc.loads == 1 && !strings.Contains(err.Error(), "configured boundary") {
				t.Fatalf("valid repeats refused offline: %v", err)
			}
			if _, e := os.Stat(outfile); !os.IsNotExist(e) {
				t.Fatal("preflight exported evidence")
			}
		})
	}
}
