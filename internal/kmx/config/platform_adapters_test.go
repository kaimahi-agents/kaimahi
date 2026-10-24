package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is a source-level contract, not Windows runtime qualification. In
// particular, an unsupported adapter must not touch its descriptor, reader,
// writer, context or filesystem before returning its refusal.
func TestUnsupportedPlatformAdaptersAreInert(t *testing.T) {
	files := []string{
		"destination_open_windows.go",
		"../app/fspolicy_windows.go",
		"../app/private_state_windows.go",
		"../agentsuite/oras/extract_windows.go",
		"../agentsuite/oras/layout_lock_windows.go",
		"../agentsuite/oras/storage_windows.go",
		"../agentsuite/workspace/storage_windows.go",
		"../toolchain/managed_windows.go",
		"../agentsuite/agentkit/buildx_windows.go",
	}
	for _, name := range files {
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			functions := 0
			for _, decl := range file.Decls {
				if _, ok := decl.(*ast.GenDecl); ok {
					if decl.(*ast.GenDecl).Tok != token.IMPORT {
						t.Fatal("adapter must not initialize or retain state")
					}
					continue
				}
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name == "init" || fn.Body == nil || len(fn.Body.List) != 1 {
					t.Fatalf("adapter must contain only an inert return: %T", decl)
				}
				functions++
				ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
				if !ok || len(ret.Results) == 0 {
					t.Fatalf("%s must return unsupported or fail closed", fn.Name.Name)
				}
				for i, result := range ret.Results {
					switch value := result.(type) {
					case *ast.Ident:
						if value.Name != "nil" && (value.Name != "false" || len(ret.Results) != 1) {
							t.Fatalf("%s must return nil handles or a false predicate", fn.Name.Name)
						}
					case *ast.BasicLit:
						if value.Kind != token.STRING || value.Value != `""` {
							t.Fatalf("%s must not return operational data", fn.Name.Name)
						}
					case *ast.CallExpr:
						if i != len(ret.Results)-1 || !platformAdapterSelector(value.Fun, "fmt", "Errorf") || len(value.Args) != 2 || !platformAdapterSelector(value.Args[1], "errors", "ErrUnsupported") {
							t.Fatalf("%s must only call fmt.Errorf wrapping errors.ErrUnsupported", fn.Name.Name)
						}
						message, ok := value.Args[0].(*ast.BasicLit)
						if !ok || message.Kind != token.STRING {
							t.Fatalf("%s must use a literal operation-specific refusal", fn.Name.Name)
						}
						text, err := strconv.Unquote(message.Value)
						if err != nil || !strings.Contains(text, ": native Windows not supported yet: %w") || strings.HasPrefix(text, ":") {
							t.Fatalf("%s must name the unsupported operation", fn.Name.Name)
						}
					default:
						t.Fatalf("%s has a potentially effectful result: %T", fn.Name.Name, result)
					}
				}
				last := ret.Results[len(ret.Results)-1]
				if _, ok := last.(*ast.CallExpr); !ok {
					predicate, ok := last.(*ast.Ident)
					if len(ret.Results) != 1 || !ok || predicate.Name != "false" {
						t.Fatalf("%s must refuse, not pretend success", fn.Name.Name)
					}
				}
			}
			if functions == 0 {
				t.Fatal("no unsupported adapters checked")
			}
		})
	}
}

func platformAdapterSelector(expr ast.Expr, pkg, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == pkg
}
