package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// platformDetectionFixture wires a fake kubectl that answers the exact
// api-resources calls detectOrkaPlatform/detectKagentV1Platform make.
// "present" reports the platform's CRD installed, "error" simulates an
// unreadable API surface, and anything else (including unset) reports
// absence — mirroring runtimeFixture's KMX_TEST_ORKA/KMX_TEST_KAGENT
// convention used by the existing #197 chat discovery tests.
func platformDetectionFixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *"--api-group=core.orka.ai"*)
    case "$KMX_TEST_ORKA_PLATFORM" in
      present) printf 'agents.core.orka.ai\n'; exit 0 ;;
      error) printf 'error: forbidden\n' >&2; exit 1 ;;
      *) exit 0 ;;
    esac ;;
  *"--api-group=kagent.dev"*)
    case "$KMX_TEST_KAGENTV1_PLATFORM" in
      present) printf 'agenttemplates.kagent.dev\n'; exit 0 ;;
      error) printf 'error: forbidden\n' >&2; exit 1 ;;
      *) exit 0 ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
}

func TestDetectOrkaPlatformReportsInstalled(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	result := a.detectOrkaPlatform(context.Background())
	if result.Err != nil || !result.Installed {
		t.Fatalf("detectOrkaPlatform() = %+v", result)
	}
}

func TestDetectOrkaPlatformReportsAbsentWithoutError(t *testing.T) {
	a := platformDetectionFixture(t)
	result := a.detectOrkaPlatform(context.Background())
	if result.Err != nil || result.Installed {
		t.Fatalf("detectOrkaPlatform() = %+v", result)
	}
}

func TestDetectOrkaPlatformFailsClosedOnReadError(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "error")
	result := a.detectOrkaPlatform(context.Background())
	if result.Err == nil || result.Installed {
		t.Fatalf("detectOrkaPlatform() = %+v", result)
	}
}

func TestDetectKagentV1PlatformReportsInstalled(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	result := a.detectKagentV1Platform(context.Background())
	if result.Err != nil || !result.Installed {
		t.Fatalf("detectKagentV1Platform() = %+v", result)
	}
}

func TestDetectKagentV1PlatformReportsAbsentWithoutError(t *testing.T) {
	a := platformDetectionFixture(t)
	result := a.detectKagentV1Platform(context.Background())
	if result.Err != nil || result.Installed {
		t.Fatalf("detectKagentV1Platform() = %+v", result)
	}
}

func TestDetectKagentV1PlatformFailsClosedOnReadError(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "error")
	result := a.detectKagentV1Platform(context.Background())
	if result.Err == nil || result.Installed {
		t.Fatalf("detectKagentV1Platform() = %+v", result)
	}
}

// End-to-end: app-supplied detector results feed the shared registry's
// platform selection exactly as DESIGN.md §1 describes — Orka preferred,
// kagent-v1 fallback, both prerequisites named when neither is installed.
func TestDetectPlatformSelectionPrefersOrka(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	id, err := agentruntime.SelectPlatform(a.detectOrkaPlatform(context.Background()), a.detectKagentV1Platform(context.Background()))
	if err != nil || id != agentruntime.Orka {
		t.Fatalf("SelectPlatform() = %q, %v", id, err)
	}
}

func TestDetectPlatformSelectionFallsBackToKagentV1(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	id, err := agentruntime.SelectPlatform(a.detectOrkaPlatform(context.Background()), a.detectKagentV1Platform(context.Background()))
	if err != nil || id != agentruntime.KagentV1 {
		t.Fatalf("SelectPlatform() = %q, %v", id, err)
	}
}

func TestDetectPlatformSelectionNamesBothPrerequisitesWhenNoneInstalled(t *testing.T) {
	a := platformDetectionFixture(t)
	_, err := agentruntime.SelectPlatform(a.detectOrkaPlatform(context.Background()), a.detectKagentV1Platform(context.Background()))
	var target *agentruntime.NoPlatformInstalledError
	if !errors.As(err, &target) {
		t.Fatalf("SelectPlatform() error = %v, not *NoPlatformInstalledError", err)
	}
	for _, want := range []string{"core.orka.ai", "kagent.dev/v1alpha3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestDetectPlatformSelectionFailsClosedOnOrkaError(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "error")
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	_, err := agentruntime.SelectPlatform(a.detectOrkaPlatform(context.Background()), a.detectKagentV1Platform(context.Background()))
	if err == nil {
		t.Fatal("an unreadable Orka detection silently fell back to kagent-v1")
	}
}
