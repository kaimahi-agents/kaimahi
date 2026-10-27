package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
)

func TestFileRunHelpNamesTestForChecks(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	if err := execute([]string{"run", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "without checks") || !strings.Contains(out.String(), "kmx test FILE") || !strings.Contains(out.String(), "only that input") {
		t.Fatalf("Run help must distinguish answers from checks: %s", out.String())
	}
}

func TestFileRunFailingCheckStillAnswers(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		t.Run(op, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateCommandHome(t))
			path := cliPreviewFile(t)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source = bytes.ReplaceAll(source, []byte("value: PRIVATE OPERAND"), []byte("value: failnow"))
			source = bytes.ReplaceAll(source, []byte("Greet the input."), []byte("Reply with exactly `Hello from Run`."))
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return privateCommandHome(t), nil }})
			err = execute([]string{op, path}, deps)
			if op == "run" {
				if err != nil || out.String() != "Hello from Run\n" || !strings.Contains(diag.String(), "ANSWERED greeting") || strings.Contains(diag.String(), "assertion_failed") {
					t.Fatalf("Run err=%v stdout=%s stderr=%s", err, out.String(), diag.String())
				}
			} else if err == nil || exitCode(err) != 1 || !strings.Contains(out.String(), "FAIL greeting") {
				t.Fatalf("Test err=%v stdout=%s stderr=%s", err, out.String(), diag.String())
			}
		})
	}
}
