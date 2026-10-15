package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePushEvaluatedDefaultAndBareValidation(t *testing.T) {
	for _, mode := range []string{"default", "false opt-out", "unevaluated", "conflict", "empty evaluation", "bare missing repository", "bare layout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			layout := filepath.Join(t.TempDir(), "layout")
			args := []string{"push", "--workspace", root, "--to-layout", layout, "agents:slide-v1"}
			want := "no completed passing evidence"
			switch mode {
			case "false opt-out":
				args = append(args, "--unevaluated=false")
			case "unevaluated":
				args = append(args, "--unevaluated")
				want = ""
			case "conflict":
				args = append(args, "--unevaluated", "--evaluation", "private.json")
				want = "cannot"
			case "empty evaluation":
				args = append(args, "--evaluation=")
				want = "non-empty --evaluation"
			case "bare missing repository":
				args = []string{"push", "--workspace", root}
				want = "publishRepository"
			case "bare layout":
				args = args[:len(args)-1]
				want = "reference"
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(args, deps)
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if *loads != 0 {
				t.Fatal("push loaded cluster configuration")
			}
			_, statErr := os.Stat(layout)
			if want != "" && !os.IsNotExist(statErr) {
				t.Fatal("refusal transferred")
			}
			if want == "" && statErr != nil {
				t.Fatal("explicit unevaluated push failed", statErr)
			}
		})
	}
}
