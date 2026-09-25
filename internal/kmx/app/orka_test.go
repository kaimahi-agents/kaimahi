package app

// `kmx orka install` against a fake cluster and a fake origin.
//
// The installer is fetched over HTTP, so these tests serve their own bytes
// and assert on the pin rather than reaching the internet: a test that
// downloaded 525 kB from a tag would be testing GitHub's availability, and
// would go red the day somebody moved the tag — which is the exact event the
// pin is supposed to report rather than suffer.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The fake answers only the reads this command makes. KMX_TEST_SECRET and
// KMX_TEST_OLLAMA switch the two branches that decide whether anything is
// written.
func TestUnreachableIsNotTheSameAsAbsent(t *testing.T) {
	for _, msg := range []string{"connection refused", "Unable to connect to the server", "no such host", "TLS handshake timeout", "context does not exist", "no configuration has been provided", "server has asked for the client to provide credentials"} {
		if !unreachable(errors.New(msg)) {
			t.Errorf("not classified unreachable: %s", msg)
		}
	}
	for _, msg := range []string{"Error from server (NotFound): deployments.apps orka not found", "Error from server (Forbidden)", "something nobody predicted"} {
		if unreachable(errors.New(msg)) {
			t.Errorf("real answer classified unreachable: %s", msg)
		}
	}
	if unreachable(nil) {
		t.Fatal("nil is not unreachable")
	}
}

const fakeOrkaKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"apply -f -"*)
    [ -n "$KMX_TEST_STDIN" ] && cat >> "$KMX_TEST_STDIN"
    exit 0 ;;
  *"get secret harness-wrapper-auth"*)
    case "$KMX_TEST_SECRET" in
      present) printf 'secret/harness-wrapper-auth\n'; exit 0 ;;
      unreachable) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *) printf 'Error from server (NotFound): secrets "harness-wrapper-auth" not found\n' >&2; exit 1 ;;
    esac ;;
  *"get svc ollama"*)
    case "$KMX_TEST_OLLAMA" in
      absent) printf 'Error from server (NotFound): services "ollama" not found\n' >&2; exit 1 ;;
      *) printf 'service/ollama\n'; exit 0 ;;
    esac ;;
  *"apply --dry-run=server -f -"*)
    case "$KMX_TEST_DRYRUN" in
      refused) printf 'error: admission webhook denied the request\n' >&2; exit 1 ;;
      *) cat >/dev/null; printf 'namespace/orka-system created (server dry run)\n'; exit 0 ;;
    esac ;;
  *"rollout status"*) printf 'deployment "x" successfully rolled out\n'; exit 0 ;;
  *"get deploy orka-controller-manager"*) printf '%s' "$KMX_TEST_IMAGE"; exit 0 ;;
  # Anchored at the END deliberately. OrkaStatus asks for "-o jsonpath={...}",
  # and an unanchored *"-o json"* pattern matches that too — which silently
  # answers the view with OrkaReady's fixture and reports Orka as absent.
  *"get deploy -o json") printf '%s' "$KMX_TEST_DEPLOY_JSON"; exit 0 ;;
  *"get deploy"*) printf '%s' "$KMX_TEST_DEPLOYMENTS"; exit 0 ;;
  *"get crd"*) printf 'tasks.core.orka.ai agents.core.orka.ai '; exit 0 ;;
  *"get providers.core.orka.ai local -o json") printf '%s' '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}'; exit 0 ;;
  *"get providers.core.orka.ai"*) printf '%s' "$KMX_TEST_PROVIDERS"; exit 0 ;;
esac
exit 0
`

type orkaFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	dir     string
	argsLog string
	stdin   string
}

// newOrkaFixture wires a fake kubectl and a fake origin serving `body`.
func newOrkaFixture(t *testing.T, body []byte) *orkaFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeOrkaKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	f := &orkaFixture{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dir: dir,
		argsLog: filepath.Join(dir, "args"), stdin: filepath.Join(dir, "stdin")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.argsLog)
	t.Setenv("KMX_TEST_STDIN", f.stdin)
	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: &config.Config{
		KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
		ContextSource: config.SourceKubeCtx,
	}, Run: r, Out: f.out, Err: f.errOut, guarded: true, orkaInstaller: srv.URL}
	return f
}

// digestOf is what the pin would be for these bytes, so a test can install a
// stand-in installer without weakening the shipped digest.
func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (f *orkaFixture) applied(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.stdin)
	if err != nil {
		return ""
	}
	return string(body)
}

func (f *orkaFixture) calls(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.argsLog)
	if err != nil {
		return ""
	}
	return string(body)
}

// The digest is of ONE version's bytes. Anything else must stop before a
// single object is written — a moved tag is a fact to report, not to install.
func TestOrkaInstallRefusesAnInstallerThatIsNotThePinnedOne(t *testing.T) {
	f := newOrkaFixture(t, []byte("kind: Namespace\n# not the pinned installer\n"))
	err := f.app.OrkaInstall(OrkaOptions{})
	if err == nil {
		t.Fatal("an unpinned installer was accepted")
	}
	for _, want := range []string{"does not hash to the digest", "Nothing was applied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if applied := f.applied(t); applied != "" {
		t.Errorf("something was applied despite the refusal:\n%s", applied)
	}
}

// The order is the one Orka's own documentation gives and the reason this
// command exists: the Secret cannot be in the manifest, and the wrapper
// mounts it at start, so it must exist first.
func TestOrkaInstallCreatesTheWrapperSecretBeforeApplyingTheInstaller(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	applied := f.applied(t)
	secretAt := strings.Index(applied, "harness-wrapper-auth")
	installerAt := strings.LastIndex(applied, "kind: Namespace")
	if secretAt < 0 {
		t.Fatalf("the wrapper Secret was never created:\n%s", applied)
	}
	if secretAt > installerAt {
		t.Error("the installer was applied before the Secret it needs at start")
	}
}

// Regenerating it under a running wrapper would rotate a token its callers
// still hold. An existing Secret is kept, and the run still succeeds.
func TestOrkaInstallKeepsAWrapperSecretThatAlreadyExists(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_SECRET", "present")

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Contains(f.applied(t), "harness-wrapper-auth") {
		t.Error("an existing wrapper Secret was overwritten")
	}
	if !strings.Contains(f.errOut.String(), "keeping it") {
		t.Errorf("the run does not say the Secret was kept:\n%s", f.errOut.String())
	}
}

// A cluster that did not answer is not a cluster without the Secret. Reading
// one as the other mints a second token under a running wrapper.
func TestOrkaInstallRefusesToGuessWhenTheSecretCannotBeRead(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_SECRET", "unreachable")

	err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
	if err == nil || !strings.Contains(err.Error(), "refusing to generate a second one") {
		t.Fatalf("an unreadable Secret was treated as absent: %v", err)
	}
}

// The Provider is the step that removes Orka's fourth prerequisite, and it
// is only honest where there is something for it to resolve against.
func TestOrkaInstallWiresAKeylessProviderAtTheModelServer(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	applied := f.applied(t)
	for _, want := range []string{
		"kind: Provider", "name: local", "type: openai",
		"baseURL: http://ollama.ollama.svc.cluster.local:11434/v1",
		"defaultModel: qwen2.5:3b",
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("the Provider does not carry %q:\n%s", want, applied)
		}
	}
}

// A Provider pointing at nothing would resolve nothing, and its refusal is
// where an adopter learns that rather than at their first model call.
func TestOrkaInstallRefusesAProviderWithNoModelServerBehindIt(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_OLLAMA", "absent")

	err := f.app.OrkaInstall(OrkaOptions{})
	if err == nil || !strings.Contains(err.Error(), "would resolve nothing") {
		t.Fatalf("a Provider was created against no endpoint: %v", err)
	}
	if !strings.Contains(err.Error(), "--provider -") {
		t.Error("the refusal does not name the way to install without one")
	}
}

// `--provider -` is the escape hatch, and it must reach the end.
func TestOrkaInstallSkipsTheProviderWhenAskedTo(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_OLLAMA", "absent")

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Contains(f.applied(t), "kind: Provider") {
		t.Error("a Provider was created despite --provider -")
	}
}

// "Installed" means both Deployments are ready. Waiting for the controller
// alone would report success while the wrapper — the half that needs the
// Secret above — was still failing to start, which is the exact outcome this
// command exists to make impossible.
func TestOrkaInstallWaitsForBothDeployments(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	calls := f.calls(t)
	for _, deployment := range []string{orkaController, orkaWrapper} {
		if !strings.Contains(calls, "rollout status deploy/"+deployment) {
			t.Errorf("the run never waited for %s:\n%s", deployment, calls)
		}
	}
}

// Installing is not governing, and the run has to say so: an adopter who
// reads "Orka is running" as "its traffic is metered" has been misled by us.
func TestOrkaInstallSaysThatInstallingGovernsNothing(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "governs nothing by itself") {
		t.Errorf("the run does not say installing governs nothing:\n%s", notes)
	}
	if !strings.Contains(notes, "kmx migrate") {
		t.Error("the run does not name the command that does govern it")
	}
}

// An absent Orka and an unreadable cluster have opposite fixes, so they must
// not print the same line.
func TestOrkaStatusSeparatesAbsentFromUnreadable(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "")
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(f.out.String(), "not installed") {
		t.Errorf("an absent Orka is not reported as absent:\n%s", f.out.String())
	}
}

// A Provider that is not there means every model call is refused, which is
// worth a line of its own rather than an empty column.
func TestOrkaStatusNamesTheConsequenceOfNoProvider(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-controller-manager=1/1 ")
	t.Setenv("KMX_TEST_PROVIDERS", "")
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(f.out.String(), "a model call would be refused") {
		t.Errorf("status does not say what no Provider costs:\n%s", f.out.String())
	}
}

// The pin is what kmx WOULD install. Restating it as the version present
// would report a version nobody installed — on a cluster somebody set up
// with their Helm chart, or with an older kmx.
func TestOrkaStatusReadsTheRunningVersionRatherThanThePin(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-controller-manager=1/1 ")
	t.Setenv("KMX_TEST_IMAGE", "ghcr.io/orka-agents/orka:0.1.2")

	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "0.1.2") {
		t.Errorf("status does not report the version that is running:\n%s", out)
	}
	if !strings.Contains(out, "installed another way") {
		t.Errorf("status does not name the disagreement with the pin:\n%s", out)
	}
}

// Their manifest tags the image `0.1.3` and the repository tags the release
// `v0.1.3`. A match must not be reported as a difference.
func TestOrkaStatusDoesNotCallAMatchingVersionASkew(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-controller-manager=1/1 ")
	t.Setenv("KMX_TEST_IMAGE", "ghcr.io/orka-agents/orka:"+strings.TrimPrefix(OrkaVersion, "v"))

	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(f.out.String(), "installed another way") {
		t.Errorf("a matching version was reported as a skew:\n%s", f.out.String())
	}
}

// --no-apply reaches the network and stops. Nothing is written, and the
// guard is never asked, because there is nothing to guard.
func TestOrkaInstallNoApplyWritesNothing(t *testing.T) {
	installer := []byte("kind: Namespace\n---\nkind: Service\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{NoApply: true}); err != nil {
		t.Fatalf("install --no-apply: %v", err)
	}
	if applied := f.applied(t); applied != "" {
		t.Errorf("--no-apply wrote to the cluster:\n%s", applied)
	}
	if !strings.Contains(f.errOut.String(), "nothing was written") {
		t.Errorf("--no-apply does not say it wrote nothing:\n%s", f.errOut.String())
	}
}

// A server dry run is how a cluster that would REFUSE the installer says so
// before it is half-applied. It writes nothing, so it skips the Secret.
func TestOrkaInstallDryRunValidatesAndWritesNothing(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{DryRun: true}); err != nil {
		t.Fatalf("install --dry-run: %v", err)
	}
	if strings.Contains(f.applied(t), "harness-wrapper-auth") {
		t.Error("--dry-run created the wrapper Secret")
	}
	if !strings.Contains(f.calls(t), "apply --dry-run=server") {
		t.Errorf("--dry-run never asked the API server:\n%s", f.calls(t))
	}
	if !strings.Contains(f.errOut.String(), "cannot show") {
		t.Error("--dry-run does not say what it could not prove")
	}
}

// A cluster that refuses the installer must fail here rather than halfway
// through applying it.
func TestOrkaInstallDryRunReportsAClusterThatWouldRefuse(t *testing.T) {
	installer := []byte("kind: Namespace\n")
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_DRYRUN", "refused")

	err := f.app.OrkaInstall(OrkaOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "would refuse") {
		t.Fatalf("a refusing cluster was not reported: %v", err)
	}
}

// The two are contradictory: one writes nothing at all, the other asks the
// cluster. Accepting both would have to silently pick one.
func TestOrkaInstallRefusesNoApplyWithDryRun(t *testing.T) {
	f := newOrkaFixture(t, nil)
	err := f.app.OrkaInstall(OrkaOptions{NoApply: true, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("the contradictory pair was accepted: %v", err)
	}
}

// orkaDeployJSON renders what `kubectl get deploy -o json` answers, so a test
// can state a rollout exactly rather than approximately.
func orkaDeployJSON(t *testing.T, deployments ...map[string]any) string {
	t.Helper()
	items := []any{}
	for _, d := range deployments {
		items = append(items, d)
	}
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// orkaDeploy is one Deployment, healthy by default, so each test states only
// the one field whose failure it is about.
func orkaDeploy(name string, edit func(meta, spec, status map[string]any)) map[string]any {
	meta := map[string]any{"name": name, "generation": 2}
	spec := map[string]any{"replicas": 1}
	status := map[string]any{"observedGeneration": 2, "updatedReplicas": 1,
		"readyReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 0}
	if edit != nil {
		edit(meta, spec, status)
	}
	return map[string]any{"metadata": meta, "spec": spec, "status": status}
}

// OrkaStatus is a VIEW: it prints "not installed" and returns nil, which is
// right for a person asking and wrong for a caller deciding whether a lift
// succeeded. `lift --payload orka` verify consults OrkaReady for exactly this
// reason, and these are the three answers that must differ.
func TestOrkaReadyRefusesAnAbsentOrka(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_JSON", "")
	t.Setenv("KMX_TEST_DEPLOYMENTS", "")

	err := f.app.OrkaReady()
	if err == nil {
		t.Fatal("an absent Orka was reported ready")
	}
	if !strings.Contains(err.Error(), "nothing was verified") {
		t.Errorf("the refusal does not say nothing was verified: %v", err)
	}

	// And the view still returns nil for the same cluster, which is why the
	// two exist separately.
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("the informational view failed: %v", err)
	}
}

// Both Deployments, or it is not installed. The wrapper is the half that needs
// the Secret created before the manifest, so a lift that checked only the
// controller would miss exactly the failure this path guards.
func TestOrkaReadyRequiresBothDeployments(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy(orkaController, nil)))

	err := f.app.OrkaReady()
	if err == nil || !strings.Contains(err.Error(), orkaWrapper) {
		t.Fatalf("a half-installed Orka passed verification: %v", err)
	}

	g := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t,
		orkaDeploy(orkaController, nil), orkaDeploy(orkaWrapper, nil)))
	if err := g.app.OrkaReady(); err != nil {
		t.Fatalf("a healthy Orka was refused: %v", err)
	}
}

// A ready replica is not a finished rollout. Every case here reports a READY
// pod — the old one — while the new spec never lands. Counting ready replicas
// alone would pass all of them, which is the failure a `--step verify` after a
// bad image is most likely to meet.
func TestOrkaReadyRefusesARolloutThatNeverLanded(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(meta, spec, status map[string]any)
		want string
	}{
		{
			// The new ReplicaSet cannot pull its image, so the old pod stays
			// ready and the updated count never reaches the desired one.
			name: "image pull backoff on the replacement",
			edit: func(_, _, status map[string]any) { status["updatedReplicas"] = 0 },
			want: "not finished rolling out",
		},
		{
			// The replacement is up but not yet serving.
			name: "replacement not available",
			edit: func(_, _, status map[string]any) { status["availableReplicas"] = 0 },
			want: "not finished rolling out",
		},
		{
			// A surge leaves one pod down; the rollout is still in flight.
			name: "one replica unavailable",
			edit: func(_, _, status map[string]any) { status["unavailableReplicas"] = 1 },
			want: "not finished rolling out",
		},
		{
			// The controller has not yet seen the spec that was applied, so
			// every count below describes the PREVIOUS spec.
			name: "spec not yet observed",
			edit: func(_, _, status map[string]any) { status["observedGeneration"] = 1 },
			want: "has not observed yet",
		},
		{
			// readyReplicas is absent, not zero, when no pod is ready. It
			// must read as not ready rather than as unparseable.
			name: "no ready replica at all",
			edit: func(_, _, status map[string]any) { delete(status, "readyReplicas") },
			want: "not finished rolling out",
		},
		{
			// Scaled to zero is not "ready", it is "nothing is running".
			name: "scaled to zero",
			edit: func(_, spec, status map[string]any) {
				spec["replicas"] = 0
				status["updatedReplicas"], status["readyReplicas"], status["availableReplicas"] = 0, 0, 0
			},
			want: "nothing is running to verify",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t,
				orkaDeploy(orkaController, tc.edit), orkaDeploy(orkaWrapper, nil)))

			err := f.app.OrkaReady()
			if err == nil {
				t.Fatal("a rollout that never landed passed verification")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %v, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), orkaController) {
				t.Errorf("the refusal does not name the deployment: %v", err)
			}
		})
	}
}

// An unreadable cluster has not been shown to be ready, and saying so is not
// the same as saying Orka is absent.
func TestOrkaReadyRefusesUnreadableJSON(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_JSON", "{not json")

	err := f.app.OrkaReady()
	if err == nil || !strings.Contains(err.Error(), "NOT been shown to be ready") {
		t.Fatalf("unreadable deployments passed verification: %v", err)
	}
}

// Every command a lift prints for the operator to paste must name the cluster.
// `aimAtTheCluster` moves only THIS process's config, so an operator whose
// current-context is still a local kind cluster would send the Secret one way
// and the Agent the other — and the half that lands locally looks like success.
//
// The key must also be read from stdin. An argument is visible in the process
// list and is usually written to shell history.
func TestOrkaNextStepsPinTheClusterAndKeepTheKeyOutOfArgv(t *testing.T) {
	for _, tc := range []struct {
		name  string
		print func(a *App)
	}{
		{"the orka phase note", func(a *App) {
			a.notef("  printf %%s \"$ORKA_API_KEY\" | kubectl --context %s -n %s \\\n"+
				"      create secret generic <name> --from-file=api-key=/dev/stdin",
				shellArg(a.Cfg.KubeContext), OrkaNamespace)
			a.notef("  %s", a.operationCommand("agent", "create", "<agent>",
				"--namespace", OrkaNamespace, "--provider-type", "openai",
				"--model", "<model>", "--secret", "<name>", "--base-url", "<endpoint>"))
		}},
		{"the closing next steps", func(a *App) {
			a.liftNextSteps(lift.Options{Payload: lift.PayloadOrka, Cluster: "demo-cluster"}, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &App{Cfg: &config.Config{KubeContext: "demo-cluster", Credential: "cred"}, Err: &buf, Out: &buf}
			tc.print(a)
			got := buf.String()

			for _, line := range strings.Split(got, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "kubectl ") && !strings.Contains(trimmed, "--context demo-cluster") {
					t.Errorf("an unpinned kubectl would use the operator's current-context: %s", trimmed)
				}
				if strings.HasPrefix(trimmed, "kmx ") && !strings.Contains(trimmed, "--context demo-cluster") {
					t.Errorf("an unpinned kmx would target the wrong cluster: %s", trimmed)
				}
			}
			if strings.Contains(got, "--from-literal=api-key") {
				t.Errorf("the key is passed in argv, where the process list and shell history see it:\n%s", got)
			}
			if !strings.Contains(got, "--from-file=api-key=/dev/stdin") {
				t.Errorf("the key is not read from protected stdin:\n%s", got)
			}
		})
	}
}

// The orka payload creates no Agent and no Provider, so the closing text must
// not claim one or send the operator to `agent chat`.
func TestOrkaClosingTextDoesNotPromiseAnAgent(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "demo-cluster", Credential: "cred"}, Err: &buf, Out: &buf}
	a.liftNextSteps(lift.Options{Payload: lift.PayloadOrka, Cluster: "demo-cluster"}, nil)
	got := buf.String()

	for _, forbidden := range []string{"agent chat", "hello-world", "The same agent you ran locally"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the orka payload promises %q, but it created no agent:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "no Provider and no Agent yet") {
		t.Errorf("the closing text does not say what is still missing:\n%s", got)
	}

	// The kagent payload still says its own thing, or this branch broke it.
	var legacy bytes.Buffer
	b := &App{Cfg: &config.Config{KubeContext: "demo-cluster", Credential: "cred"}, Err: &legacy, Out: &legacy}
	b.liftNextSteps(lift.Options{Payload: lift.PayloadKagent, Cluster: "demo-cluster"}, nil)
	if !strings.Contains(legacy.String(), "hello-world") {
		t.Errorf("the kagent payload lost its next steps:\n%s", legacy.String())
	}
}

// A verify with observability off still differs by payload: the orka payload
// makes no model call, so promising an agent answer there is a false claim.
func TestTheNoTelemetryPhaseLabelIsPayloadAware(t *testing.T) {
	orka := liftVerifyPurposeWithoutTelemetry(lift.PayloadOrka)
	if strings.Contains(orka, "agent answers") {
		t.Errorf("an orka verify promises an agent answer: %q", orka)
	}
	if !strings.Contains(orka, "Orka") {
		t.Errorf("an orka verify does not say what it checked: %q", orka)
	}
	if kagent := liftVerifyPurposeWithoutTelemetry(lift.PayloadKagent); !strings.Contains(kagent, "agent answers") {
		t.Errorf("the kagent verify lost its purpose: %q", kagent)
	}
}
