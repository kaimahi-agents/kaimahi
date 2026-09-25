package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The fake cluster boundary for `kmx up`. The legacy read answers the way a
// cluster WITHOUT the kagent CRDs answers — "the server doesn't have a
// resource type" — so any run that touches it fails, and a run that completes
// has demonstrably not touched it.
const fakeUpKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"config view -o json"*)
    printf '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}'
    exit 0 ;;
  *kagent.dev*)
    printf 'error: the server doesn'"'"'t have a resource type "agents"\n' >&2
    exit 1 ;;
  *"apply -f -"*) cat >/dev/null; exit 0 ;;
  *"get secret harness-wrapper-auth"*)
    printf 'Error from server (NotFound): secrets "harness-wrapper-auth" not found\n' >&2
    exit 1 ;;
  *"get svc ollama"*) printf 'service/ollama\n'; exit 0 ;;
esac
exit 0
`

// upFixture wires kind, kubectl and the container engine to fakes, and the
// pinned Orka installer to bytes this test owns.
func upFixture(t *testing.T) (*App, *bytes.Buffer, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake cluster boundary is a shell script")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeUpKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, dir, "kind", `printf 'kind %s\n' "$*" >> "$KMX_TEST_ARGS"; exit 0`)
	fakeTool(t, dir, "docker", `printf 'docker %s\n' "$*" >> "$KMX_TEST_ARGS"; exit 0`)
	// The real PATH stays behind the fakes: the fake kubectl is a shell
	// script and needs the shell's own utilities, while the fakes still win
	// the lookup for every tool this command runs.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("KMX_TEST_ARGS", args)

	installer := []byte("kind: Namespace\n---\nkind: Deployment\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(installer)
	}))
	t.Cleanup(server.Close)

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{
		Cfg: &config.Config{KindCluster: "test", KubeContext: "kind-test",
			ContextSource: config.SourceKubeCtx, ContainerEngine: "docker",
			Model: "qwen2.5:3b", Credential: "hello-world"},
		Run: &run.Runner{Stdout: out, Stderr: errOut}, Out: out, Err: errOut,
		guarded: true, orkaInstaller: server.URL, orkaInstallerDigest: digestOf(installer),
	}
	return a, errOut, args
}

func upCalls(t *testing.T, args string) string {
	t.Helper()
	body, err := os.ReadFile(args)
	if err != nil {
		return ""
	}
	return string(body)
}

// `kmx up` is the Orka runtime and nothing else, so it must never ask this
// cluster about the legacy runtime. The fake answers that read the way a
// cluster with no kagent CRD answers — the command completing at all is the
// proof that it was not asked.
func TestBareOrkaUpCompletesWithoutQueryingTheLegacyRuntime(t *testing.T) {
	a, errOut, args := upFixture(t)
	if err := a.Up(""); err != nil {
		t.Fatalf("a bare Orka-only `kmx up` failed: %v\n%s", err, errOut)
	}
	calls := upCalls(t, args)
	if strings.Contains(calls, "kagent") || strings.Contains(calls, "modelconfigs") {
		t.Errorf("a bare `kmx up` read the legacy runtime:\n%s", calls)
	}
	for _, want := range []string{"COMPLETE", "Runtime setup finished", "[4/4]"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the run did not report %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut.String(), "Collect runtime status") {
		t.Errorf("a bare `kmx up` still collects the legacy status:\n%s", errOut)
	}
}

// The guidance at the end of an Orka-only run has to be a route this cluster
// can take. Putting an application's model traffic on the seam is
// `kmx migrate`, one workload at a time.
func TestOrkaOnlyUpPointsAtMigrateRatherThanGovern(t *testing.T) {
	a, errOut, _ := upFixture(t)
	if err := a.Up(""); err != nil {
		t.Fatalf("a bare Orka-only `kmx up` failed: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut.String(), "kmx --context kind-test migrate") {
		t.Errorf("the Orka-only run offers no migration route:\n%s", errOut)
	}
	if strings.Contains(errOut.String(), a.operationCommand("govern")) {
		t.Errorf("the Orka-only run recommends governing an agent it did not deploy:\n%s", errOut)
	}
}

// The addressable steps ARE the supported sequence. The legacy installer
// steps are gone, so `--step kagent` is an unknown step that fails locally
// rather than a hidden install path behind a name.
func TestUpStepsAreExactlyTheSupportedSequence(t *testing.T) {
	want := []string{"cluster", "ollama", "model", "orka"}
	if strings.Join(UpSteps, ",") != strings.Join(want, ",") {
		t.Fatalf("UpSteps is %q, want %q", UpSteps, want)
	}
	if strings.Join(UpDefaultSteps, ",") != strings.Join(want, ",") {
		t.Fatalf("UpDefaultSteps is %q, want %q", UpDefaultSteps, want)
	}
}

// Naming a retired installer step must be refused before anything is
// provisioned, and the refusal must list what can be run instead. Refusing
// locally matters more than the wording: the step is gone, so reaching a
// cluster to discover that would be a round trip for nothing.
func TestRetiredInstallerStepsAreRefusedWithoutReachingACluster(t *testing.T) {
	for _, step := range []string{"kagent", "agent", "tools-agent"} {
		t.Run(step, func(t *testing.T) {
			empty := t.TempDir()
			t.Setenv("PATH", empty)
			var out bytes.Buffer
			a := &App{Cfg: &config.Config{KindCluster: "test", KubeContext: "kind-test"},
				Run: &run.Runner{}, Out: &out, Err: &out}
			err := a.Up(step)
			if err == nil {
				t.Fatalf("the retired step %q was accepted", step)
			}
			if !strings.Contains(err.Error(), "unknown step") || !strings.Contains(err.Error(), "orka") {
				t.Fatalf("the refusal does not name the steps that exist: %v", err)
			}
		})
	}
}

// Helm was fetched and preflighted for one reason: installing the legacy
// chart. With that gone, no step may ask for it — a prerequisite nobody needs
// is a download on somebody's machine for nothing.
func TestNoUpStepRequiresHelm(t *testing.T) {
	for _, step := range append([]string{""}, UpSteps...) {
		steps := UpDefaultSteps
		if step != "" {
			steps = []string{step}
		}
		a := &App{Cfg: &config.Config{ContainerEngine: "docker"}}
		for _, dep := range a.upDependencies(steps) {
			if dep.name == "helm" {
				t.Fatalf("step %q still asks for helm", step)
			}
		}
	}
}
