//go:build ignore

// Check initialization in Orka routing exceptions, including app live-test binaries.
// Run from the repository root: go run scripts/ci/orka-boundary.go.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	if err := check(); err != nil {
		// Source snippets and initializer arguments are never diagnostics.
		fmt.Fprintln(os.Stderr, "Orka initialization boundary failed")
		os.Exit(1)
	}
	fmt.Println("Orka initialization boundary verified")
}

func check() error {
	var policy struct {
		Prefixes []string `json:"safe_prefixes"`
		Files    []string `json:"safe_files"`
	}
	data, err := os.ReadFile("scripts/ci/orka-routing.json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &policy); err != nil {
		return err
	}
	files := map[string]bool{}
	qualified := func(path string) bool {
		// App tests are linked into the live test binary even when -run selects
		// another test. Other leaf packages' tests are not linked into Orka.
		return strings.HasSuffix(path, ".go") && (!strings.HasSuffix(path, "_test.go") ||
			strings.HasPrefix(path, "internal/kmx/app/"))
	}
	for _, prefix := range policy.Prefixes {
		if err := filepath.WalkDir(prefix, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && qualified(path) {
				files[path] = true
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, path := range policy.Files {
		if qualified(path) {
			files[path] = true
		}
	}
	for path := range files {
		if err := checkFile(path); err != nil {
			return err
		}
	}
	return nil
}

func checkFile(path string) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return err
	}
	imports := map[string]string{}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return err
		}
		name := filepath.Base(value)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." {
			return fmt.Errorf("implicit initialization import")
		}
		imports[name] = value
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "init" && fn.Recv == nil {
			return fmt.Errorf("init function")
		}
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, value := range spec.(*ast.ValueSpec).Values {
				if !passive(value, imports) {
					return fmt.Errorf("active global initializer")
				}
			}
		}
	}
	return nil
}

func passive(expr ast.Expr, imports map[string]string) bool {
	ok := true
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncLit:
			// A stored closure does not execute during initialization. Calling it
			// is rejected at its enclosing CallExpr, like every arbitrary call.
			return false
		case *ast.UnaryExpr:
			if n.Op == token.ARROW {
				ok = false
			}
		case *ast.CallExpr:
			// A same-file type's pointer-to-nil conversion is a passive interface
			// assertion, not a call through a potentially active function variable.
			if paren, ok := n.Fun.(*ast.ParenExpr); ok && len(n.Args) == 1 {
				star, pointer := paren.X.(*ast.StarExpr)
				arg, nilArg := n.Args[0].(*ast.Ident)
				if pointer && nilArg && arg.Name == "nil" && !n.Ellipsis.IsValid() {
					if typ, named := star.X.(*ast.Ident); named && typ.Obj != nil && typ.Obj.Kind == ast.Typ {
						return false
					}
				}
			}
			selector, selected := n.Fun.(*ast.SelectorExpr)
			if !selected || len(n.Args) != 1 || n.Ellipsis.IsValid() {
				ok = false
				return false
			}
			pkg, qualified := selector.X.(*ast.Ident)
			arg, literal := n.Args[0].(*ast.BasicLit)
			if !qualified || !literal || arg.Kind != token.STRING || pkg.Obj != nil ||
				!((imports[pkg.Name] == "errors" && selector.Sel.Name == "New") ||
					(imports[pkg.Name] == "regexp" && selector.Sel.Name == "MustCompile")) {
				ok = false
			}
		}
		return ok
	})
	return ok
}
