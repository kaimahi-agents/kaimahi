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
// discovery calls detectOrkaPlatform/detectKagentV1Platform make.
// KMX_TEST_ORKA_PLATFORM=present/error selects Orka's api-resources answer.
// KMX_TEST_KAGENTV1_PLATFORM selects detectKagentV1Platform's versioned
// discovery answer for exactly "get --raw /apis/kagent.dev/v1alpha3":
//   - present: that exact group-version is served and lists AgentTemplate
//     (kagent-v1 installed).
//   - other-version: the server has kagent.dev resources (e.g. legacy
//     kagent's v1alpha2 Agent) but nothing registers v1alpha3 itself, so
//     the apiserver's own discovery answers NotFound for this specific
//     group-version — proving the detector does not accept an unrelated
//     served version.
//   - error: a hard discovery failure (not a 404) that must fail closed.
//   - unset (default): nothing under kagent.dev at all; same NotFound shape
//     as other-version, since from this endpoint's perspective total
//     absence and "some other version only" are indistinguishable — both
//     correctly mean "v1alpha3 AgentTemplate is not installed".
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
  *"get --raw /apis/kagent.dev/v1alpha3"*)
    case "$KMX_TEST_KAGENTV1_PLATFORM" in
      present) printf '{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"kagent.dev/v1alpha3","resources":[{"name":"agenttemplates","singularName":"agenttemplate","namespaced":true,"kind":"AgentTemplate","verbs":["get","list","watch"]}]}\n'; exit 0 ;;
      error) printf 'Unable to connect to the server: dial tcp 127.0.0.1:6443: i/o timeout\n' >&2; exit 1 ;;
      *) printf 'Error from server (NotFound): the server could not find the requested resource\n' >&2; exit 1 ;;
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

// Fix round 1, HIGH finding: the detector must prove the exact served
// kagent.dev/v1alpha3 AgentTemplate support, not merely that some
// agenttemplates.kagent.dev resource exists under any version. These four
// tests pin that contract directly against the versioned discovery
// document.
func TestDetectKagentV1PlatformAcceptsExactServedV1Alpha3(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "present")
	result := a.detectKagentV1Platform(context.Background())
	if result.Err != nil || !result.Installed {
		t.Fatalf("detectKagentV1Platform() = %+v", result)
	}
}

// A kagent.dev AgentTemplate served only under some other version (for
// example legacy kagent's v1alpha2, which never defines AgentTemplate at
// all, or a hypothetical future version) must never be accepted as
// kagent-v1: the group existing is not the same as v1alpha3 being served.
func TestDetectKagentV1PlatformRejectsOtherServedVersion(t *testing.T) {
	a := platformDetectionFixture(t)
	t.Setenv("KMX_TEST_KAGENTV1_PLATFORM", "other-version")
	result := a.detectKagentV1Platform(context.Background())
	if result.Err != nil || result.Installed {
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

func TestDetectKagentV1PlatformFailsClosedOnDiscoveryError(t *testing.T) {
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
