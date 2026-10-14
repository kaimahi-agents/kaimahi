package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubectlFixtureMatchesParentBuildMode(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), kubectlFixtureBinary(t), "-test.run=^TestFixtureBuildModeHelper$")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper build mode: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), fmt.Sprintf("race=%t", kubectlFixtureRace); got != want {
		t.Fatalf("helper build mode = %q, want %q", got, want)
	}
}

func TestKubectlFixtureSharedProtocolMatchesApp(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), kubectlFixtureBinary(t), "-test.run=^TestFixtureProtocolHelper$")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper protocol: %v: %s", err, out)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode helper protocol: %v: %s", err, out)
	}
	want := map[string]string{
		"namespace":         OrkaNamespace,
		"resultAccount":     orkaResultAccount,
		"portableMarker":    orkaPortableMarker,
		"renderedMarker":    orkaRenderedMarker,
		"token":             orkaTestToken(),
		"providerPlural":    orkaPlural("Provider"),
		"annotationPointer": orkaAnnotationPointer("a~/b"),
		"workerController":  workerController("controller", "orka-0.2.0", "orka", "Helm", `["--ai-worker-service-account-name=worker"]`),
		"workerAccount":     workerAccount("worker", OrkaNamespace, "orka-0.2.0", "orka", "ai"),
	}
	if !maps.Equal(got, want) {
		t.Fatalf("helper protocol=%v, want App protocol=%v", got, want)
	}
}

func TestKubectlFixtureKeepsExecutableArgumentBoundary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KMX_ORKA_TEST_DIR", dir)
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestOrkaKubectlHelper"))
	cmd := exec.CommandContext(t.Context(), filepath.Join(dir, "kubectl"), "version", "--client")
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("fake kubectl argument boundary: err=%v output=%q", err, out)
	}
}
