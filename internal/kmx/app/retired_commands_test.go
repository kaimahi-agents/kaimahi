package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A retired command must not be SUGGESTED, not merely refused when typed.
//
// `cmd/kmx` proves removed commands are refused. The other half is next-step
// text from successful native operations: it must not send an operator to a
// command the binary refuses to run.
//
// So the rule is checked where such text is produced: every operationCommand
// call in this package's production files, whose first argument is a literal
// verb. Only literals are judged — a verb assembled at run time is not
// something this scan can read, and guessing would make it fail on unrelated
// code.
func TestNoProductionCodeSuggestsARetiredCommand(t *testing.T) {
	// The retired verbs, and what an operator should be sent to instead. The
	// replacement is named so a failure says what to write, not merely what
	// not to.
	retired := map[string]string{
		"govern":      "native Agents use their runtime Provider directly",
		"use":         "there is no legacy preset switch",
		"plane":       "native setup uses kmx up or kmx orka install",
		"migrate":     "native Agents use their runtime Provider directly",
		"models":      "model selection belongs to native Provider setup",
		"credential":  "native Provider Secrets are provisioned separately",
		"credentials": "native Provider Secrets are provisioned separately",
		"ledger":      "the model-plane client is removed",
		"budget":      "the model-plane client is removed",
		"flow":        "the model-plane client is removed",
		"watch":       "the model-plane client is removed",
		"backup":      "the model-plane client is removed",
		"restore":     "the model-plane client is removed",
		"metrics":     "the model-plane client is removed",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	foundNative := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "operationCommand" || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			checked++
			verb := strings.Trim(lit.Value, `"`)
			if verb == "agent" && len(call.Args) > 1 {
				if sub, ok := call.Args[1].(*ast.BasicLit); ok && sub.Kind == token.STRING && strings.Trim(sub.Value, `"`) == "create" {
					foundNative = true
				}
			}
			if instead, dead := retired[verb]; dead {
				t.Errorf("%s: suggests the retired command `kmx %s` — %s",
					fset.Position(call.Pos()), verb, instead)
			}
			return true
		})
	}
	// Without this the scan could pass by reading nothing at all, which is
	// exactly how a guard like this rots.
	if checked == 0 || !foundNative {
		t.Fatalf("scan lost native agent-create calls (%d verbs checked) — it is passing vacuously", checked)
	}
}

// The same rule for the documentation an operator is pointed at. docs/kmx.md
// is the command reference; a retired command described there as something to
// run is a promise the binary refuses to keep.
func TestTheCommandReferenceDoesNotInstructARetiredCommand(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "kmx.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Backticked invocations only. Prose explaining that `kmx govern` HAS BEEN
	// REMOVED is the correct thing for this page to say, and a plain substring
	// search would refuse exactly that sentence.
	for _, dead := range []string{"`kmx govern <", "`kmx govern `", "`kmx use `", "`kmx use <"} {
		if strings.Contains(string(body), dead) {
			t.Errorf("docs/kmx.md instructs the retired invocation %s", dead)
		}
	}
	// And the retired lift payload is not offered as a thing to select.
	if strings.Contains(string(body), "pass `--payload kagent`") {
		t.Error("docs/kmx.md still offers the retired kagent payload as a choice")
	}
}

// The same rule for text an operator READS rather than a command they are
// offered structurally. `operationCommand` is not the only way to name a
// command: `kmx govern` reached operators as plain output from `models add`
// and from the certificate publisher's missing-namespace note, neither of
// which builds an operationCommand.
//
// String literals only. Comments explaining that a command WAS retired are
// exactly what these files should say, and a whole-file search would refuse
// the sentence that documents the retirement.
func TestNoOperatorFacingStringNamesARetiredCommand(t *testing.T) {
	retired := []string{"kmx govern", "kmx use", "kmx agent edit", "kmx plane", "kmx migrate", "kmx models", "kmx credential", "kmx credentials", "kmx ledger", "kmx budget", "kmx flow", "kmx watch", "kmx backup", "kmx restore", "kmx metrics"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			scanned++
			for _, dead := range retired {
				if strings.Contains(lit.Value, dead) {
					t.Errorf("%s: operator-facing text names the retired command %q: %s",
						fset.Position(lit.Pos()), strings.TrimSpace(dead), lit.Value)
				}
			}
			return true
		})
	}
	// Negative control on the scan itself: a guard that reads nothing
	// passes forever.
	if scanned < 500 {
		t.Fatalf("only %d string literals were examined — the scan is passing vacuously", scanned)
	}
}
