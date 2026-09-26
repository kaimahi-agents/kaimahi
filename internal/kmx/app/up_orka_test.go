package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The fake cluster boundary for `kmx up`. The combined kagent read answers
// the way a cluster WITHOUT the kagent CRDs answers — "the server doesn't
// have a resource type" — so a bare Orka-only run that touches it fails, and
// a run that completes has demonstrably not touched it.
const fakeUpKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"config view -o json"*)
    printf '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}'
    exit 0 ;;
  *"get agents.kagent.dev,modelconfigs,pods"*)
    case "$KMX_TEST_KAGENT" in
      installed) printf '{"items":[]}'; exit 0 ;;
      *) printf 'error: the server doesn'"'"'t have a resource type "agents"\n' >&2; exit 1 ;;
    esac ;;
  *"get agents.kagent.dev hello-world"*)
    printf 'Error from server (NotFound): agents.kagent.dev "hello-world" not found\n' >&2
    exit 1 ;;
  *"apply -f -"*)
    body=$(cat)
    case "$body" in *"kind: Provider"*) printf '%s' "$body" > "$KMX_TEST_ARGS.provider" ;; esac
    exit 0 ;;
  *"get providers.core.orka.ai local --ignore-not-found=true -o json"*|*"get providers.core.orka.ai local -o json"*)
    if [ "$KMX_TEST_PROVIDER_READ" = failed ]; then exit 1; fi
    url="$KMX_TEST_PROVIDER_URL"
    if [ -z "$url" ] && [ -f "$KMX_TEST_ARGS.provider" ]; then url='http://ollama.ollama.svc.cluster.local:11434/v1'; fi
    [ -n "$url" ] || exit 0
    observed=2
    [ "$KMX_TEST_PROVIDER_READY" = stale ] && observed=1
    printf '{"apiVersion":"core.orka.ai/v1alpha1","kind":"Provider","metadata":{"name":"local","namespace":"orka-system","uid":"provider-1","generation":2},"spec":{"baseURL":"%s"},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":%s}]}}' "$url" "$observed"
    exit 0 ;;
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

// A bare `kmx up` is the Orka runtime and nothing else, so it must never ask
// this cluster about the legacy runtime. The fake answers that read the way a
// cluster with no kagent CRD answers — the command completing at all is the
// proof that it was not asked. The phase count is the same claim from the
// operator's side: five phases were counted when the fifth was a kagent read.
func TestBareOrkaUpCompletesWithoutQueryingTheLegacyRuntime(t *testing.T) {
	a, errOut, args := upFixture(t)
	if err := a.Up(""); err != nil {
		t.Fatalf("a bare Orka-only `kmx up` failed: %v\n%s", err, errOut)
	}
	calls := upCalls(t, args)
	if strings.Contains(calls, "agents.kagent.dev") || strings.Contains(calls, "modelconfigs") {
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

// A host route shared by migrated workloads cannot be silently replaced by
// either entry point that runs stepOrka. The refusal identifies both routes
// and offers an explicit, context-pinned command to deliberately reset it.
func TestOrkaSetupRefusesToReplaceAnExistingLocalEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*App) error
	}{
		{"quickstart", func(a *App) error { return a.quickstartSteps()[3].fn() }},
		{"up --step orka", func(a *App) error { return a.Up("orka") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, args := upFixture(t)
			t.Setenv("KMX_TEST_PROVIDER_URL", "http://172.18.0.1:11434/v1")
			err := tc.run(a)
			if err == nil {
				t.Fatal("overwrote the existing host endpoint")
			}
			for _, want := range []string{"http://172.18.0.1:11434/v1", orkaDefaultModelURL, "kmx --context kind-test orka install", "--model-url"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal missing %q: %v", want, err)
				}
			}
			if _, err := os.Stat(args + ".provider"); !os.IsNotExist(err) {
				t.Fatalf("Provider was written despite endpoint drift: %v", err)
			}
		})
	}
}

// Bare up must not say complete merely because the Provider was submitted;
// readiness must be observed for the generation just applied.
func TestBareUpWaitsForCurrentGenerationProviderReady(t *testing.T) {
	a, _, args := upFixture(t)
	if err := a.Up(""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upCalls(t, args), "get providers.core.orka.ai local -o json") {
		t.Fatal("no post-apply Provider readiness read")
	}

	stale, errOut, _ := upFixture(t)
	t.Setenv("KMX_TEST_PROVIDER_READY", "stale")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stale.Run.Context = ctx
	if err := stale.Up(""); err == nil || !strings.Contains(err.Error(), "current-generation Ready") {
		t.Fatalf("stale Provider was considered ready: %v\n%s", err, errOut)
	}
}

// The guidance at the end of an Orka-only run has to be a route this cluster
// can take. `kmx govern` configures a kagent Agent's ModelConfig, and a bare
// run deploys no kagent Agent; putting an application's model traffic on the
// seam is `kmx migrate`.
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

// The legacy flow keeps its status. An explicitly requested kagent step is
// the one place that read is about something this run put there, so the
// summary stays exactly there and nowhere else.
func TestExplicitLegacyStepStillCollectsTheKagentStatus(t *testing.T) {
	a, errOut, args := upFixture(t)
	t.Setenv("KMX_TEST_KAGENT", "installed")
	if err := a.Up("agent"); err != nil {
		t.Fatalf("the explicit legacy step failed: %v\n%s", err, errOut)
	}
	if !strings.Contains(upCalls(t, args), "get agents.kagent.dev,modelconfigs,pods") {
		t.Errorf("the legacy step lost its status collection:\n%s", upCalls(t, args))
	}
	if !strings.Contains(errOut.String(), "Collect runtime status") {
		t.Errorf("the legacy step reported no status phase:\n%s", errOut)
	}
}

// The predicate itself, so the rule is readable as one list rather than
// inferred from two integration runs.
func TestOnlyLegacyStepsAskForTheKagentStatus(t *testing.T) {
	for _, step := range []string{"cluster", "ollama", "model", "orka"} {
		if stepsIncludeLegacy([]string{step}) {
			t.Errorf("step %q was classified as legacy", step)
		}
	}
	for _, step := range []string{"kagent", "agent", "tools-agent"} {
		if !stepsIncludeLegacy([]string{step}) {
			t.Errorf("legacy step %q was classified as Orka-only", step)
		}
	}
	if stepsIncludeLegacy(UpDefaultSteps) {
		t.Errorf("a bare `kmx up` runs a legacy step: %q", UpDefaultSteps)
	}
}
