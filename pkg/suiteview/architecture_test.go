package suiteview_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPublicPackageKeepsOperationalDependenciesOut(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	for _, dependency := range strings.Fields(string(output)) {
		for _, forbidden := range []string{
			"os/exec",
			"github.com/kaimahi-agents/kaimahi/cmd/",
			"github.com/kaimahi-agents/kaimahi/internal/kmx/app",
			"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras",
			"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace",
			"github.com/kaimahi-agents/kaimahi/internal/kmx/config",
			"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime",
			"github.com/spf13/",
			"k8s.io/",
			"oras.land/oras-go/v2/registry",
		} {
			if dependency == forbidden || strings.HasPrefix(dependency, forbidden) {
				t.Errorf("public read-only loader depends on operational package %q", dependency)
			}
		}
	}
}

func TestWorkspaceUsesPublicProjectionWithoutReverseDependency(t *testing.T) {
	const module = "github.com/kaimahi-agents/kaimahi/"
	imports := func(packagePath string) []string {
		t.Helper()
		output, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, packagePath).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(output))
		}
		return strings.Fields(string(output))
	}
	workspace := imports(module + "internal/kmx/agentsuite/workspace")
	if !contains(workspace, module+"pkg/suiteview") {
		t.Fatal("native workspace loading does not use the public suiteview projection")
	}
	view := imports(module + "pkg/suiteview")
	if contains(view, module+"internal/kmx/agentsuite") {
		t.Fatal("public suiteview projection depends on its former internal implementation")
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
