package kmx_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

var forbiddenImports = []string{
	"github.com/kaimahi-agents/kaimahi/internal/",
	"github.com/Azure/",
	"k8s.io/",
	"sigs.k8s.io/",
	"os/exec",
	"github.com/spf13/cobra",
	"charm.land/bubbletea/",
	"charm.land/lipgloss/",
	"golang.org/x/term",
}

var forbiddenConcreteNames = []string{
	"aks",
	"azure",
	"cobra",
	"kagent",
	"kube",
	"orka",
	"subprocess",
	"bubbletea",
	"lipgloss",
}

var forbiddenSerializedNames = []string{
	"backend",
	"command",
	"context",
	"namespace",
	"provider",
	"resourcekind",
}

func TestProductionSurfaceIsImplementationNeutral(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		inspectImports(t, path, file)
		inspectDeclarations(t, path, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func inspectImports(t *testing.T, path string, file *ast.File) {
	t.Helper()
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range forbiddenImports {
			if strings.HasPrefix(strings.ToLower(importPath), strings.ToLower(forbidden)) {
				t.Errorf("%s imports implementation dependency %q", path, importPath)
			}
		}
	}
}

func inspectDeclarations(t *testing.T, path string, file *ast.File) {
	t.Helper()
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			inspectExportedName(t, path, declaration.Name.Name)
		case *ast.GenDecl:
			for _, raw := range declaration.Specs {
				switch spec := raw.(type) {
				case *ast.TypeSpec:
					inspectExportedName(t, path, spec.Name.Name)
					ast.Inspect(spec.Type, func(node ast.Node) bool {
						field, ok := node.(*ast.Field)
						if !ok {
							return true
						}
						for _, name := range field.Names {
							inspectExportedName(t, path, name.Name)
						}
						if field.Tag != nil {
							inspectSerializedName(t, path, field.Tag.Value)
						}
						return true
					})
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						inspectExportedName(t, path, name.Name)
					}
				}
			}
		}
	}
}

func inspectExportedName(t *testing.T, path, name string) {
	t.Helper()
	if !ast.IsExported(name) {
		return
	}
	lower := strings.ToLower(name)
	for _, forbidden := range forbiddenConcreteNames {
		if strings.Contains(lower, forbidden) {
			t.Errorf("%s exports concrete implementation name %q", path, name)
		}
	}
}

func inspectSerializedName(t *testing.T, path, rawTag string) {
	t.Helper()
	tag, err := strconv.Unquote(rawTag)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range strings.Fields(tag) {
		_, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		value, err = strconv.Unquote(value)
		if err != nil {
			continue
		}
		lower := strings.ToLower(strings.Split(value, ",")[0])
		for _, forbidden := range append(forbiddenConcreteNames, forbiddenSerializedNames...) {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s exposes implementation-shaped serialized name %q", path, lower)
			}
		}
	}
}

func TestDestructiveWorkflowSignaturesKeepReceiptScopesSeparate(t *testing.T) {
	t.Parallel()
	assertMethodSet(t, reflect.TypeOf((*kmx.EnvironmentService)(nil)).Elem(), "Down", "Forget", "Inspect", "RecoverDown", "RecoverUp", "Register", "Up")
	assertMethodSet(t, reflect.TypeOf((*kmx.AgentService)(nil)).Elem(), "Build", "Lift", "RecoverLift", "RecoverRetire", "Retire", "Status")
	assertMethodSet(t, reflect.TypeOf((*kmx.PlatformDeprovisioner)(nil)).Elem(), "Deprovision")
	assertMethodSet(t, reflect.TypeOf((*kmx.RuntimeRetirer)(nil)).Elem(), "Retire")
	assertMethodSignature(t, reflect.TypeOf((*kmx.EnvironmentService)(nil)).Elem(), "Down", reflect.TypeOf(kmx.DownRequest{}), reflect.TypeOf(kmx.TeardownReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.PlatformDeprovisioner)(nil)).Elem(), "Deprovision", reflect.TypeOf(kmx.DeprovisionRequest{}), reflect.TypeOf(kmx.TeardownReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.AgentService)(nil)).Elem(), "Retire", reflect.TypeOf(kmx.RetireRequest{}), reflect.TypeOf(kmx.RetirementReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.RuntimeRetirer)(nil)).Elem(), "Retire", reflect.TypeOf(kmx.RetireRequest{}), reflect.TypeOf(kmx.RetirementReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.EnvironmentService)(nil)).Elem(), "RecoverUp", reflect.TypeOf(kmx.OperationID("")), reflect.TypeOf(kmx.UpProgress{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.EnvironmentService)(nil)).Elem(), "RecoverDown", reflect.TypeOf(kmx.OperationID("")), reflect.TypeOf(kmx.TeardownReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.AgentService)(nil)).Elem(), "RecoverLift", reflect.TypeOf(kmx.OperationID("")), reflect.TypeOf(kmx.DeploymentReceipt{}))
	assertMethodSignature(t, reflect.TypeOf((*kmx.AgentService)(nil)).Elem(), "RecoverRetire", reflect.TypeOf(kmx.OperationID("")), reflect.TypeOf(kmx.RetirementReceipt{}))
}

func assertMethodSet(t *testing.T, contract reflect.Type, want ...string) {
	t.Helper()
	if contract.NumMethod() != len(want) {
		t.Fatalf("%s has %d methods, want %d", contract, contract.NumMethod(), len(want))
	}
	for _, name := range want {
		if _, ok := contract.MethodByName(name); !ok {
			t.Errorf("%s has no %s method", contract, name)
		}
	}
}

func assertMethodSignature(t *testing.T, contract reflect.Type, methodName string, input, output reflect.Type) {
	t.Helper()
	method, ok := contract.MethodByName(methodName)
	if !ok {
		t.Fatalf("%s has no %s method", contract, methodName)
	}
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if method.Type.NumIn() != 2 || method.Type.In(0) != contextType || method.Type.In(1) != input ||
		method.Type.NumOut() != 2 || method.Type.Out(0) != output || method.Type.Out(1) != errorType {
		t.Fatalf("%s.%s signature = %s, want func(context.Context, %s) (%s, error)", contract, methodName, method.Type, input, output)
	}
}

func TestDependencyClosureUsesOnlyStandardLibrary(t *testing.T) {
	t.Parallel()
	command := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, output)
	}
	const ownPackage = "github.com/kaimahi-agents/kaimahi/pkg/kmx"
	for _, dependency := range strings.Fields(string(output)) {
		if dependency != ownPackage && !strings.HasPrefix(dependency, ownPackage+"/") {
			t.Errorf("public contract depends on non-standard package %q", dependency)
		}
	}
}
