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
// `cmd/kmx` proves that `kmx govern`, `kmx use` and `kmx agent edit` are
// unknown. That is the easier half. The half that actually reaches an operator
// is the NEXT-step text a successful command prints: `kmx plane` closed with
// "Govern the agent — kmx govern <credential>" long after the command it named
// had gone, which is worse than an unknown command, because the operator has
// been told to run it by something that just succeeded.
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
		"govern": "`kmx migrate` routes an application's model traffic through the plane",
		"use":    "there is no preset switch; the legacy runtime it switched is gone",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
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
			if instead, dead := retired[verb]; dead {
				t.Errorf("%s: suggests the retired command `kmx %s` — %s",
					fset.Position(call.Pos()), verb, instead)
			}
			return true
		})
	}
	// Without this the scan could pass by reading nothing at all, which is
	// exactly how a guard like this rots.
	if checked < 20 {
		t.Fatalf("only %d literal operationCommand verbs were examined — the scan is passing vacuously", checked)
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
