package lifecycle_test

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/lifecycle"
	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

func TestDependencyClosureContainsOnlyPublicContractAndStandardLibrary(t *testing.T) {
	t.Parallel()
	command := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, output)
	}
	const (
		ownPackage    = "github.com/kaimahi-agents/kaimahi/internal/kmx/lifecycle"
		publicPackage = "github.com/kaimahi-agents/kaimahi/pkg/kmx"
	)
	for _, dependency := range strings.Fields(string(output)) {
		if dependency != ownPackage && dependency != publicPackage {
			t.Errorf("internal lifecycle contract depends on %q", dependency)
		}
	}
}

func TestDestructivePortsKeepReceiptScopesSeparate(t *testing.T) {
	t.Parallel()
	assertMethodSignature(t,
		reflect.TypeOf((*lifecycle.PlatformDeprovisioner)(nil)).Elem(),
		"Deprovision", reflect.TypeOf(lifecycle.DeprovisionRequest{}), reflect.TypeOf(kmx.TeardownReceipt{}),
	)
	assertMethodSignature(t,
		reflect.TypeOf((*lifecycle.RuntimeRetirer)(nil)).Elem(),
		"Retire", reflect.TypeOf(kmx.RetireRequest{}), reflect.TypeOf(kmx.RetirementReceipt{}),
	)
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
		t.Fatalf("%s.%s signature = %s", contract, methodName, method.Type)
	}
}
