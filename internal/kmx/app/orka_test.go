package app

// `kmx orka install` against a fake cluster and a fake origin.
//
// The installer is fetched over HTTP, so these tests serve their own bytes
// and assert on the pin rather than reaching the internet: a test that
// downloaded 525 kB from a tag would be testing GitHub's availability, and
// would go red the day somebody moved the tag — which is the exact event the
// pin is supposed to report rather than suffer.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

// These names are fixture identities, never production controller selectors.
const (
	orkaController      = "orka-controller-manager"
	orkaChartController = "orka-api-controller"
)

// The fake answers only the reads this command makes. KMX_TEST_OLLAMA
// selects whether a Provider can resolve a model endpoint.
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
printf 'kubectl %s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"create --raw /apis/authorization.k8s.io/v1/subjectaccessreviews -f -"*)
    [ "$KMX_TEST_SAR_ALLOW" = 1 ] || exit 1
    cat >/dev/null
    printf '{"status":{"allowed":true}}'
    exit 0 ;;
  *"apply -f -"*)
    [ -n "$KMX_TEST_STDIN" ] && cat >> "$KMX_TEST_STDIN"
    exit 0 ;;
  *"get secret harness-wrapper-auth"*)
    printf 'Error from server (NotFound): secrets "harness-wrapper-auth" not found\n' >&2; exit 1 ;;
  *"get svc ollama"*)
    case "$KMX_TEST_OLLAMA" in
      absent) printf 'Error from server (NotFound): services "ollama" not found\n' >&2; exit 1 ;;
      *) printf 'service/ollama\n'; exit 0 ;;
    esac ;;
  *"get providers.core.orka.ai local --ignore-not-found=true -o json"*|*"get providers.core.orka.ai local -o json"*)
    [ -f "$KMX_TEST_STDIN" ] || exit 0
    grep -q 'kind: Provider' "$KMX_TEST_STDIN" || exit 0
    printf '{"kind":"Provider","metadata":{"name":"local","namespace":"orka-system","uid":"provider-1","generation":1},"spec":{"baseURL":"http://ollama.ollama.svc.cluster.local:11434/v1"},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}'
    exit 0 ;;
  *"get outboundaccesspolicies.core.orka.ai kmx-k8s-tool-gateway -o json"*)
    printf '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Accepted","status":"True","observedGeneration":1}]}}'
    exit 0 ;;
  *"get tools.core.orka.ai k8s-get-resources -o json"*)
    printf '{"metadata":{"generation":1},"status":{"conditions":[{"type":"Available","status":"True","observedGeneration":1}]}}'
    exit 0 ;;
  *"apply --dry-run=server -f -"*)
    case "$KMX_TEST_DRYRUN" in
      refused) printf 'error: admission webhook denied the request\n' >&2; exit 1 ;;
      *) cat >/dev/null; printf 'namespace/orka-system created (server dry run)\n'; exit 0 ;;
    esac ;;
  *"rollout status"*)
    if [ -n "$KMX_TEST_ROLLOUT_TARGET" ]; then
      case "$*" in
        *"rollout status deploy/$KMX_TEST_ROLLOUT_TARGET "*) ;;
        *) printf 'Error from server (NotFound): deployment target not found\n' >&2; exit 1 ;;
      esac
    fi
    printf 'deployment "x" successfully rolled out\n'; exit 0 ;;
  # Anchored at the END deliberately. OrkaStatus asks for "-o jsonpath={...}",
  # and an unanchored *"-o json"* pattern matches that too — which silently
  # answers the view with OrkaReady's fixture and reports Orka as absent.
  *"get deploy -o jsonpath="*)
    case "$KMX_TEST_DEPLOY_READ" in
      forbidden) printf 'Error from server (Forbidden): deployment list refused (fixture-sensitive-token)\n' >&2; exit 1 ;;
      unexpected) printf 'unexpected kubectl failure (fixture-sensitive-token)\n' >&2; exit 1 ;;
    esac
    printf '%s' "$KMX_TEST_DEPLOYMENTS"; exit 0 ;;
  *"get serviceaccounts -o json")
    printf '%s' "$KMX_TEST_WORKER_ACCOUNTS"; exit 0 ;;
  *"get deploy -o json")
    if [ "$KMX_TEST_DEPLOY_READ" = notfound ]; then
      printf 'Error from server (NotFound): namespaces "orka-system" not found (credential-shaped=fixture-sensitive-token)\n' >&2
      exit 1
    fi
    printf '%s' "$KMX_TEST_DEPLOY_JSON"; exit 0 ;;
  *"get deployment"*|*"get deploy"*)
    if [ -n "$KMX_TEST_EXISTING_DEPLOY" ]; then printf '%s' "$KMX_TEST_EXISTING_DEPLOY"; exit 0; fi
    printf '%s' "$KMX_TEST_DEPLOYMENTS"; exit 0 ;;
  *"get namespace orka-system"*) printf '%s' "$KMX_TEST_EXISTING_NS"; exit 0 ;;
  *"get crd tasks.core.orka.ai"*)
    [ "$KMX_TEST_EXISTING_CRD" = tasks.core.orka.ai ] && printf '%s' "$KMX_TEST_EXISTING_CRD"
    exit 0 ;;
  *"get crd agents.core.orka.ai"*)
    [ "$KMX_TEST_EXISTING_CRD" = agents.core.orka.ai ] && printf '%s' "$KMX_TEST_EXISTING_CRD"
    exit 0 ;;
  *"get crd"*) printf '%s' "$KMX_TEST_EXISTING_CRD"; exit 0 ;;
  *"get providers.core.orka.ai"*) printf '%s' "$KMX_TEST_PROVIDERS"; exit 0 ;;
esac
exit 0
`

const fakeOrkaHelm = `#!/bin/sh
printf 'helm %s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"list "*) printf '%s' "${KMX_TEST_HELM_LIST:-[]}"; exit 0 ;;
  *"get values "*)
    if [ "$KMX_TEST_HELM_VALUES_READ" = failed ]; then printf 'helm failure (fixture-sensitive-token)\n' >&2; exit 1; fi
    printf '%s' "$KMX_TEST_HELM_VALUES"; exit 0 ;;
  *"install "*)
    case "$*" in
      *"--dry-run"*)
        if [ "$KMX_TEST_DRYRUN" = refused ]; then printf 'error: admission webhook denied the request\n' >&2; exit 1; fi
        exit 0 ;;
    esac
    printf 'helm install\n' >> "$KMX_TEST_WRITES"
    if [ "$KMX_TEST_HELM_INSTALL" = failed ]; then printf 'error: fixture-sensitive-token\n' >&2; exit 1; fi
    # Simulate sensitive chart NOTES: installer output must never copy a key.
    printf 'harness-v2-key=fixture-sensitive-token\n'
    exit 0 ;;
esac
exit 0
`

// This is a real gzip'd Helm chart, not a YAML manifest with a .tgz suffix.
// The version and CRD are deliberately inside the archive so install tests
// detect code that validates only the download digest then skips chart data.
func orkaChart(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tarball := tar.NewWriter(gz)
	for _, file := range []struct{ name, body string }{
		{"orka/Chart.yaml", "apiVersion: v2\nname: orka\nversion: 0.2.0\nappVersion: 0.2.0\n"},
		{"orka/crds/tasks.core.orka.ai.yaml", "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: tasks.core.orka.ai\n"},
		{"orka/templates/controller.yaml", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: orka-controller-manager\n"},
	} {
		if err := tarball.WriteHeader(&tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

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
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(fakeOrkaHelm), 0o755); err != nil {
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
	t.Setenv("KMX_TEST_WRITES", filepath.Join(dir, "writes"))
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

func (f *orkaFixture) writes(t *testing.T) string {
	t.Helper()
	calls := f.calls(t)
	var writes []string
	for _, call := range strings.Split(calls, "\n") {
		if (strings.HasPrefix(call, "helm install ") && !strings.Contains(call, "--dry-run")) || strings.Contains(call, " apply -f -") || strings.Contains(call, " create secret ") {
			writes = append(writes, call)
		}
	}
	return strings.Join(writes, "\n")
}

// The digest is of ONE version's bytes. Anything else must stop before a
// single object is written — a moved tag is a fact to report, not to install.
func TestOrkaInstallRefusesAnInstallerThatIsNotThePinnedOne(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, append(chart, []byte("tampered")...))
	f.app.orkaInstallerDigest = digestOf(chart)
	err := f.app.OrkaInstall(OrkaOptions{})
	if err == nil {
		t.Fatal("an unpinned installer was accepted")
	}
	for _, want := range []string{"does not hash to the digest", "Nothing was applied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if writes := f.writes(t); writes != "" {
		t.Errorf("the digest mismatch wrote to the cluster:\n%s", writes)
	}
}

// The old raw-manifest install cannot be adopted by a Helm install. Refuse
// before writes even when no Helm release is registered in the namespace.
func TestOrkaInstallRefusesExistingV013BeforeAnyWrites(t *testing.T) {
	for _, tc := range []struct{ name, deployment, crd string }{
		{"deployment", "deployment.apps/orka-controller-manager", ""},
		{"orphaned Task CRD", "", "tasks.core.orka.ai"},
		{"orphaned Provider CRD", "", "providers.core.orka.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			t.Setenv("KMX_TEST_EXISTING_DEPLOY", tc.deployment)
			t.Setenv("KMX_TEST_EXISTING_CRD", tc.crd)

			err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
			if err == nil {
				t.Fatal("installed Helm on top of existing manifest resources")
			}
			for _, want := range []string{f.app.operationCommand("local", "down"), f.app.operationCommand("local", "up"), "Tasks", "Secrets", "model data", "ledger", "AKS", "snapshot key"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("legacy recovery does not say %q: %v", want, err)
				}
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("existing v0.1.3 caused writes before refusal:\n%s", writes)
			}
			if !strings.Contains(f.calls(t), "helm list") {
				t.Errorf("existing cluster was not checked for a Helm release:\n%s", f.calls(t))
			}
		})
	}
}

func TestOrkaInstallAllowsPrecreatedNamespaceWithoutOrkaWorkloads(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_EXISTING_NS", "namespace/orka-system")
	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("precreated namespace refused: %v", err)
	}
	if !strings.Contains(f.calls(t), "helm install orka ") {
		t.Fatal("chart was not installed into precreated namespace")
	}
}

func TestOrkaInstallRefusesForeignOrMismatchedHelmRelease(t *testing.T) {
	for _, tc := range []struct {
		name, releases string
	}{
		{"foreign release", `[{"name":"orka","namespace":"orka-system","chart":"another-chart-0.2.0","app_version":"0.2.0","status":"deployed"}]`},
		{"mismatched version", `[{"name":"orka","namespace":"orka-system","chart":"orka-0.1.3","app_version":"0.1.3","status":"deployed"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			t.Setenv("KMX_TEST_HELM_LIST", tc.releases)
			err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
			if err == nil {
				t.Fatal("adopted or replaced a different Helm release")
			}
			for _, want := range []string{f.app.operationCommand("local", "down"), f.app.operationCommand("local", "up"), "Tasks", "AKS", "snapshot key"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("foreign release recovery does not say %q: %v", want, err)
				}
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("a foreign release caused writes:\n%s", writes)
			}
			if !strings.Contains(f.calls(t), "helm list -n orka-system --kube-context kind-kaimahi-p1 -o json -f ^orka$") {
				t.Errorf("release ownership check was not scoped to the cluster/name:\n%s", f.calls(t))
			}
		})
	}
}

func pinnedOrkaHelmValues(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	values := map[string]any{
		"labels":           map[string]any{orkaChartLabel: "true"},
		"fullnameOverride": "orka-api",
		"controller": map[string]any{
			"mode": "harness-v2", "image": map[string]any{"digest": orkaControllerDigest},
			"acpRuntime": map[string]any{
				"codexImage":    "ghcr.io/orka-agents/orka/acp-codex-runtime@sha256:1f3f52eaa2c17403219f595f99bfad2e4d2d66f861921357fbd939825fc113ea",
				"claudeImage":   "ghcr.io/orka-agents/orka/acp-claude-runtime@sha256:ec8b51083626c14d1dd206fc6a57ecd6f5aeabf10522fc665903fdb205543e92",
				"copilotImage":  "ghcr.io/orka-agents/orka/acp-copilot-runtime@sha256:32983da321bb03ef57eb80508454117a0142485782e28e935297dcd9234133ab",
				"opencodeImage": "ghcr.io/orka-agents/orka/acp-opencode-runtime@sha256:3d8e84b811834d768785055fef63ea1f1179eb7cb0b0bc1856bbf81023c48f9a",
			},
		},
		"workers":   map[string]any{"ai": map[string]any{"image": map[string]any{"digest": orkaAIWorkerDigest}}, "general": map[string]any{"image": map[string]any{"digest": orkaGeneralWorkerDigest}}},
		"publisher": map[string]any{"image": map[string]any{"digest": orkaPublisherDigest}},
	}
	if mutate != nil {
		mutate(values)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOrkaInstallFailedReleaseNamesRecoverySteps(t *testing.T) {
	for _, state := range []string{"failed", "pending-install", "uninstalling"} {
		t.Run(state, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"`+state+`"}]`)
			t.Setenv("KMX_TEST_HELM_VALUES", pinnedOrkaHelmValues(t, nil))
			err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
			if err == nil {
				t.Fatal("non-deployed release was accepted")
			}
			for _, want := range []string{"kmx-owned", state, "helm --kube-context kind-kaimahi-p1", f.app.operationCommand("local", "down"), f.app.operationCommand("local", "up"), "Tasks", "AKS", "snapshot key"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in recovery: %v", want, err)
				}
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("partial release was overwritten: %s", writes)
			}
		})
	}
}

func TestOrkaInstallProvisionsTheResultReaderForDemo(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatal(err)
	}
	applied := f.applied(t)
	for _, want := range []string{"name: orka-result-reader", "kind: RoleBinding", `resources: ["tasks"]`, `verbs: ["get"]`} {
		if !strings.Contains(applied, want) {
			t.Errorf("standalone install omitted result reader %q", want)
		}
	}
}

func TestOrkaInstallUnavailableReleaseValuesRefusesWithSafeRecovery(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"pending-install"}]`)
	t.Setenv("KMX_TEST_HELM_VALUES_READ", "failed")
	err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
	if err == nil {
		t.Fatal("unreadable release values accepted")
	}
	for _, want := range []string{f.app.operationCommand("local", "down"), "AKS", "helm --kube-context kind-kaimahi-p1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unreadable release lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error()+f.errOut.String(), "fixture-sensitive-token") {
		t.Fatal("Helm stderr leaked possible credential")
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("unreadable release mutated: %s", writes)
	}
}

func TestOrkaInstallKeepsIdentifiedChartAndReconcilesReader(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
	t.Setenv("KMX_TEST_HELM_VALUES", pinnedOrkaHelmValues(t, nil))
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage("w112-controller", "ghcr.io/orka-agents/orka@"+orkaControllerDigest)))
	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("repeat install: %v", err)
	}
	if writes := f.writes(t); strings.Contains(writes, "helm install") || strings.Contains(f.applied(t), "kind: CustomResourceDefinition") {
		t.Fatalf("repeat install mutated chart or CRDs: %s", writes)
	}
	if !strings.Contains(f.applied(t), "kind: RoleBinding") {
		t.Fatal("repeat did not reconcile the read-only result account")
	}
}

func TestOrkaInstallRepeatRejectsLiveControllerImageDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []any
	}{
		{"patched controller", []any{map[string]any{"name": "controller", "image": "ghcr.io/foreign/orka@sha256:other"}}},
		{"missing controller", []any{map[string]any{"name": "sidecar", "image": "ghcr.io/orka-agents/orka@" + orkaControllerDigest}}},
		{"no containers", []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
			t.Setenv("KMX_TEST_HELM_VALUES", pinnedOrkaHelmValues(t, nil))
			controller := orkaDeploy("w112-controller", nil)
			controller["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"] = tc.images
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, controller))
			err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
			if err == nil || !strings.Contains(err.Error(), "live Orka controller image") {
				t.Fatalf("unpinned live controller accepted: %v", err)
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("live image drift caused writes: %s", writes)
			}
		})
	}
}

func TestOrkaInstallRefusesChangedChartImageValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"controller", func(v map[string]any) {
			v["controller"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:other"
		}},
		{"AI worker", func(v map[string]any) {
			v["workers"].(map[string]any)["ai"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:other"
		}},
		{"general worker", func(v map[string]any) {
			v["workers"].(map[string]any)["general"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:other"
		}},
		{"publisher", func(v map[string]any) {
			v["publisher"].(map[string]any)["image"].(map[string]any)["digest"] = "sha256:other"
		}},
		{"ACP runtime", func(v map[string]any) {
			v["controller"].(map[string]any)["acpRuntime"].(map[string]any)["codexImage"] = "ghcr.io/foreign/runtime@sha256:other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chart := orkaChart(t)
			f := newOrkaFixture(t, chart)
			f.app.orkaInstallerDigest = digestOf(chart)
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
			t.Setenv("KMX_TEST_HELM_VALUES", pinnedOrkaHelmValues(t, tc.change))
			err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("changed image accepted: %v", err)
			}
			if writes := f.writes(t); writes != "" {
				t.Fatalf("modified foreign release: %s", writes)
			}
		})
	}
}

func TestOrkaInstallRepeatRefusesUnreadyController(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
	t.Setenv("KMX_TEST_HELM_VALUES", pinnedOrkaHelmValues(t, nil))
	// Keep the image pinned so this test isolates rollout readiness.
	d := orkaDeployWithImage("w112-controller", "ghcr.io/orka-agents/orka@"+orkaControllerDigest)
	d["status"].(map[string]any)["availableReplicas"] = 0
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, d))
	err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
	if err == nil || !strings.Contains(err.Error(), "not finished rolling out") {
		t.Fatalf("unready release reported running: %v", err)
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("repair silently changed release: %s", writes)
	}
	if strings.Contains(f.errOut.String(), "COMPLETE") {
		t.Fatal("unready release reported success")
	}
}

func TestOrkaInstallRefusesUnmarkedChart(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.2.0","app_version":"v0.2.0","status":"deployed"}]`)
	t.Setenv("KMX_TEST_HELM_VALUES", `{"controller":{"mode":"harness-v2"},"fullnameOverride":"orka-api"}`)
	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err == nil {
		t.Fatal("foreign chart adopted without kmx marker")
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("foreign chart mutated: %s", writes)
	}
}

// Fresh install applies chart CRDs before Helm, pins the chosen cluster, and
// selects the harness-v2 controller. Credentials must not appear in output.
func TestOrkaInstallAppliesChartCRDsBeforeHelm(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("fresh chart install: %v\n%s", err, f.errOut)
	}
	calls := f.calls(t)
	crd := strings.Index(calls, "kubectl --context kind-kaimahi-p1 apply -f -")
	helm := strings.Index(calls, "helm install orka ")
	if crd < 0 || helm < 0 || crd > helm {
		t.Fatalf("chart CRDs must be applied before Helm install:\n%s", calls)
	}
	wait := strings.Index(calls, "kubectl --context kind-kaimahi-p1 wait --for=condition=Established")
	if wait < crd || wait > helm {
		t.Fatalf("CRDs were not Established before Helm Gateway discovery:\n%s", calls)
	}
	if !strings.Contains(f.applied(t), "name: tasks.core.orka.ai") {
		t.Errorf("chart CRDs were not submitted to kubectl:\n%s", f.applied(t))
	}
	for _, want := range []string{"--kube-context kind-kaimahi-p1", "-n orka-system", "--create-namespace", "--wait", "--timeout 10m", "controller.mode=harness-v2", "--set-string labels.kmx\\.kaimahi\\.ai/installed=true"} {
		if !strings.Contains(calls[helm:], want) {
			t.Errorf("Helm install omitted %q:\n%s", want, calls[helm:])
		}
	}
	if strings.Contains(calls, "get secret harness-wrapper-auth") || strings.Contains(f.applied(t), "harness-wrapper-auth") {
		t.Errorf("legacy wrapper Secret was read or written:\n%s\n%s", calls, f.applied(t))
	}
	for _, output := range []string{f.out.String(), f.errOut.String(), calls} {
		if strings.Contains(output, "fixture-sensitive-token") {
			t.Fatalf("harness-v2 key was exposed: %s", output)
		}
	}
}

func TestOrkaInstallFailedChartNamesSafeDiagnosticsWithoutLeakingOutput(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)
	t.Setenv("KMX_TEST_HELM_INSTALL", "failed")
	err := f.app.OrkaInstall(OrkaOptions{Provider: "-"})
	if err == nil {
		t.Fatal("failed Helm install passed")
	}
	for _, want := range []string{"helm --kube-context kind-kaimahi-p1", "kubectl --context kind-kaimahi-p1", "failed release"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing diagnostic %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error()+f.errOut.String()+f.out.String(), "fixture-sensitive-token") {
		t.Fatal("Helm stderr exposed possible credential")
	}
}

// The Provider is the step that removes Orka's fourth prerequisite, and it
// is only honest where there is something for it to resolve against.
func TestOrkaInstallWiresAKeylessProviderAtTheModelServer(t *testing.T) {
	installer := orkaChart(t)
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
	installer := orkaChart(t)
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
	installer := orkaChart(t)
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

// Helm's --wait is the readiness boundary for the v0.2.0 chart; the
// v0.1.3 wrapper must not be treated as part of the new install.
func TestOrkaInstallWaitsForHelmChart(t *testing.T) {
	chart := orkaChart(t)
	f := newOrkaFixture(t, chart)
	f.app.orkaInstallerDigest = digestOf(chart)

	if err := f.app.OrkaInstall(OrkaOptions{Provider: "-"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "--wait") || !strings.Contains(calls, "--timeout 10m") {
		t.Errorf("Helm install did not wait for readiness:\n%s", calls)
	}
	if strings.Contains(calls, "rollout status deploy/orka-agent-harness-wrapper") {
		t.Errorf("new chart install waited for the retired wrapper:\n%s", calls)
	}
}

// A successful native install points to native status, not removed commands.
func TestOrkaInstallOffersNativeStatusWithoutPlaneAdvice(t *testing.T) {
	installer := orkaChart(t)
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "kmx orka status") {
		t.Errorf("install did not offer native status:\n%s", notes)
	}
	for _, forbidden := range []string{"kmx migrate", "kmx plane", "seam"} {
		if strings.Contains(notes, forbidden) {
			t.Errorf("install advertises %q:\n%s", forbidden, notes)
		}
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
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy(orkaController, nil)))
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
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage(orkaController, "ghcr.io/orka-agents/orka:0.1.2")))

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

// The pinned image tag omits the leading `v` in OrkaVersion. A match must
// not be reported as a difference.
func TestOrkaStatusDoesNotCallAMatchingVersionASkew(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-controller-manager=1/1 ")
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage(orkaController, "ghcr.io/orka-agents/orka:"+strings.TrimPrefix(OrkaVersion, "v"))))

	if err := f.app.OrkaStatus(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(f.out.String(), "installed another way") {
		t.Errorf("a matching version was reported as a skew:\n%s", f.out.String())
	}
}

func TestOrkaStatusParsesImageTagAfterRegistryPort(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "w112-controller=1/1 ")
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage("w112-controller", "registry.example:5000/orka:0.2.0")))
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.Contains(out, "version running        0.2.0") || strings.Contains(out, "installed another way") {
		t.Fatalf("misparsed tagged controller image: %s", out)
	}
}

func TestOrkaStatusRefusesDeploymentListFailures(t *testing.T) {
	for _, issue := range []string{"forbidden", "unexpected"} {
		t.Run(issue, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_DEPLOY_READ", issue)
			err := f.app.OrkaStatus()
			if err == nil || !strings.Contains(err.Error(), "cannot read Orka") || strings.Contains(err.Error(), "fixture-sensitive-token") {
				t.Fatalf("read failure mishandled: %v", err)
			}
			if issue == "forbidden" && !strings.Contains(err.Error(), "forbidden") {
				t.Fatalf("lost permission failure: %v", err)
			}
			if strings.Contains(f.out.String(), "not installed") {
				t.Fatal("failed read reported absence")
			}
		})
	}
}

func TestOrkaStatusRefusesAmbiguousControllerLabels(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "two-controllers=1/1 ")
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy(orkaController, nil), orkaDeploy("w112-controller", nil)))
	if err := f.app.OrkaStatus(); err == nil || !strings.Contains(err.Error(), "multiple Orka controller Deployments") {
		t.Fatalf("ambiguous Orka status: %v", err)
	}
}

func TestOrkaStatusReadsPinnedDigestControllerVersion(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-api-controller=1/1 ")
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage("w112-controller", "ghcr.io/orka-agents/orka@"+orkaControllerDigest)))
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "0.2.0") || strings.Contains(f.out.String(), "unknown") {
		t.Fatalf("pinned digest not recognized: %s", f.out)
	}
}

func TestOrkaStatusReadsChartControllerVersion(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOYMENTS", "orka-api-controller=1/1 ")
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeployWithImage("w112-controller", "ghcr.io/orka-agents/orka:0.2.0")))
	if err := f.app.OrkaStatus(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "0.2.0") || strings.Contains(f.out.String(), "unknown") {
		t.Fatalf("chart version not found: %s", f.out)
	}
}

// --no-apply reaches the network and stops. Nothing is written, and the
// guard is never asked, because there is nothing to guard.
func TestOrkaInstallNoApplyWritesNothing(t *testing.T) {
	installer := orkaChart(t)
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{NoApply: true}); err != nil {
		t.Fatalf("install --no-apply: %v", err)
	}
	if writes := f.writes(t); writes != "" {
		t.Errorf("--no-apply wrote to the cluster:\n%s", writes)
	}
	if !strings.Contains(f.errOut.String(), "nothing was written") {
		t.Errorf("--no-apply does not say it wrote nothing:\n%s", f.errOut.String())
	}
	if strings.Contains(f.errOut.String(), "See them:  curl") || !strings.Contains(f.errOut.String(), "tar -tzf") {
		t.Errorf("--no-apply gives an unusable chart inspection hint:\n%s", f.errOut.String())
	}
}

// Dry-run validates against the cluster without applying chart CRDs or
// creating a Helm release.
func TestOrkaInstallDryRunValidatesAndWritesNothing(t *testing.T) {
	installer := orkaChart(t)
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)

	if err := f.app.OrkaInstall(OrkaOptions{DryRun: true}); err != nil {
		t.Fatalf("install --dry-run: %v", err)
	}
	if writes := f.writes(t); writes != "" {
		t.Errorf("--dry-run made cluster writes:\n%s", writes)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "--dry-run") {
		t.Errorf("--dry-run never validated the chart or CRDs:\n%s", calls)
	}
	if !strings.Contains(f.errOut.String(), "nothing was written") {
		t.Errorf("--dry-run did not explain its no-write boundary:\n%s", f.errOut)
	}
}

// A cluster that refuses the installer must fail here rather than halfway
// through applying it.
func TestOrkaInstallDryRunReportsAClusterThatWouldRefuse(t *testing.T) {
	installer := orkaChart(t)
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	t.Setenv("KMX_TEST_DRYRUN", "refused")

	err := f.app.OrkaInstall(OrkaOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "would refuse") {
		t.Fatalf("a refusing cluster was not reported: %v", err)
	}
	if writes := f.writes(t); writes != "" {
		t.Fatalf("a refused dry run wrote to the cluster:\n%s", writes)
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
func orkaDeployWithImage(name, image string) map[string]any {
	d := orkaDeploy(name, nil)
	spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["containers"].([]any)[0].(map[string]any)["image"] = image
	return d
}

func orkaDeploy(name string, edit func(meta, spec, status map[string]any)) map[string]any {
	labels := map[string]any{"app.kubernetes.io/name": "orka"}
	if name == orkaController {
		labels["control-plane"] = "controller-manager"
	}
	if name != orkaController && name != orkaWrapper {
		labels["app.kubernetes.io/component"] = "controller"
	}
	meta := map[string]any{"name": name, "generation": 2, "labels": labels}
	spec := map[string]any{"replicas": 1}
	status := map[string]any{"observedGeneration": 2, "updatedReplicas": 1,
		"readyReplicas": 1, "availableReplicas": 1, "unavailableReplicas": 0}
	if edit != nil {
		edit(meta, spec, status)
	}
	// Legacy readiness checks must keep recognizing the v0.1.3 manifest.
	spec["template"] = map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "controller", "image": "ghcr.io/orka-agents/orka:0.1.3"}}}}
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

// Legacy v0.1.3 installs still require both Deployments. The new chart does
// not create a wrapper, but existing consumer verification must not mistake
// a half-installed manifest runtime for a healthy legacy installation.
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

func TestOrkaControllerForLiftSelectsLabelsNotNames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		controller string
	}{
		{"legacy v0.1.3", orkaController}, {"chart other release name", "w112-controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy(tc.controller, nil)))
			t.Setenv("KMX_TEST_ROLLOUT_TARGET", tc.controller)
			got, err := f.app.orkaControllerForLift(t.Context())
			if err != nil || got != tc.controller {
				t.Fatalf("controller = %s, %v; want %s", got, err, tc.controller)
			}
			if _, err := f.app.orkaCapture(t.Context(), nil, "-n", OrkaNamespace, "rollout", "status", "deploy/"+got, "--timeout=10s"); err != nil {
				t.Fatalf("wrong rollout target: %v", err)
			}
			if calls := f.calls(t); !strings.Contains(calls, "rollout status deploy/"+tc.controller+" ") {
				t.Fatalf("did not query selected controller: %s", calls)
			}
		})
	}
}

func TestOrkaControllerDiscoveryNamesMissingNamespaceWithoutLeakingStderr(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_READ", "notfound")
	_, err := f.app.orkaControllerForLift(t.Context())
	if err == nil || !strings.Contains(err.Error(), "namespace orka-system not found") || strings.Contains(err.Error(), "fixture-sensitive-token") {
		t.Fatalf("unsanitized or unhelpful discovery error: %v", err)
	}
}

func TestOrkaControllerForLiftRefusesZeroAndMultipleMatches(t *testing.T) {
	for _, tc := range []struct {
		name, want  string
		deployments []map[string]any
	}{
		{"zero", "no Orka controller Deployment", []map[string]any{orkaDeploy(orkaWrapper, nil)}},
		{"unrelated controller", "no Orka controller Deployment", []map[string]any{func() map[string]any {
			d := orkaDeploy("other-controller", nil)
			d["metadata"].(map[string]any)["labels"].(map[string]any)["app.kubernetes.io/name"] = "unrelated"
			return d
		}()}},

		{"ambiguous", "multiple Orka controller Deployments", []map[string]any{orkaDeploy(orkaController, nil), orkaDeploy("w112-controller", nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, tc.deployments...))
			_, err := f.app.orkaControllerForLift(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("controller discovery = %v; want %s", err, tc.want)
			}
			if strings.Contains(f.calls(t), "rollout status") {
				t.Fatal("rolled out an ambiguous or absent controller")
			}
		})
	}
}

func TestOrkaReadyAcceptsHarnessV2ControllerWithoutLegacyWrapper(t *testing.T) {
	f := newOrkaFixture(t, nil)
	t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, orkaDeploy("w112-controller", nil)))
	if err := f.app.OrkaReady(); err != nil {
		t.Fatalf("chart controller was refused without a v1 wrapper: %v", err)
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
			a.liftNextSteps(lift.Options{Payload: lift.PayloadOrka, Cluster: "demo-cluster"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &App{Cfg: &config.Config{KubeContext: "demo-cluster"}, Err: &buf, Out: &buf}
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

// The lift creates no Agent and no Provider, so the closing text must not
// claim one or send the operator to `agent chat`.
func TestOrkaClosingTextDoesNotPromiseAnAgent(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "demo-cluster"}, Err: &buf, Out: &buf}
	a.liftNextSteps(lift.Options{Payload: lift.PayloadOrka, Cluster: "demo-cluster"})
	got := buf.String()

	for _, forbidden := range []string{"agent chat", "hello-world", "The same agent you ran locally"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the closing text promises %q, but the lift created no agent:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "no Provider and no Agent yet") {
		t.Errorf("the closing text does not say what is still missing:\n%s", got)
	}
}

// A verify with observability off still has to say what it actually proved.
// The lift makes no model call, so promising an agent answer is a false claim.
func TestTheNoTelemetryPhaseLabelPromisesNoAnswer(t *testing.T) {
	label := liftVerifyPurposeWithoutTelemetry()
	if strings.Contains(label, "agent answers") {
		t.Errorf("a verify promises an agent answer: %q", label)
	}
	if !strings.Contains(label, "Orka") {
		t.Errorf("a verify does not say what it checked: %q", label)
	}
}

// `kmx up --step orka` is the step that OWNS the Task result identity.
//
// Result reads go over Orka's API with a short-lived token for this account,
// so it has to exist before any command can retrieve an answer — and the
// grant has to be reviewable. `kmx agent create` deliberately only NAMES an
// existing account: minting an identity as a side effect of authoring an
// agent would hide a grant inside a command nobody reads as a grant.
func TestUpOrkaStepOwnsTheResultAccountAndItsExactGrant(t *testing.T) {
	installer := orkaChart(t)
	f := newOrkaFixture(t, installer)
	f.app.orkaInstallerDigest = digestOf(installer)
	f.app.Cfg.Model = "qwen2.5:3b"

	if err := f.app.stepOrka(); err != nil {
		t.Fatalf("step orka: %v", err)
	}
	applied := f.applied(t)
	for _, want := range []string{
		"kind: ServiceAccount",
		"name: " + orkaResultAccount,
		"kind: Role",
		"kind: RoleBinding",
		`apiGroups: ["core.orka.ai"]`,
		`resources: ["tasks"]`,
		`verbs: ["get"]`,
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("the result account manifest is missing %q:\n%s", want, applied)
		}
	}
	// The ceiling, stated. A second verb or a second resource here is a
	// widened grant, and a token for this account carries its full effective
	// authority — so the extent is asserted, not merely the presence.
	for _, forbidden := range []string{"secrets", "pods", `"list"`, `"watch"`, `"create"`, `"delete"`, "ClusterRole"} {
		if strings.Contains(applied, forbidden) {
			t.Errorf("the result account grant was widened with %q:\n%s", forbidden, applied)
		}
	}
	// The whole step, not just its last object: a run that skipped the
	// installer or the Provider would still have written the account.
	calls := f.calls(t)
	for _, want := range []string{"helm install orka", "controller.mode=harness-v2", "get svc ollama"} {
		if !strings.Contains(calls, want) {
			t.Errorf("the orka step did not %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(applied, "kind: Provider") || !strings.Contains(applied, orkaDefaultModelURL) {
		t.Errorf("the orka step wired no keyless Provider at the in-cluster endpoint:\n%s", applied)
	}
}

// Every path that needs the Task result identity writes the SAME grant.
//
// `kmx up --step orka`, `kmx quickstart --interactive` and the lift deployment all
// provision the account results are read with, and a copy per path is free to
// drift: a Role whose extent depends on which command wrote it is a grant
// nobody reviews as one. The wizard used to carry its own literal; both
// halves are asserted here because the next path is one paste away from a
// second spelling.
func TestEveryPathWritesTheSameResultReaderGrant(t *testing.T) {
	installer := orkaChart(t)

	// The grant itself, applied by nothing else, so the comparison below is
	// byte-for-byte rather than "mentions the same words somewhere".
	canonical := newOrkaFixture(t, installer)
	if err := canonical.app.orkaResultReader(); err != nil {
		t.Fatalf("result reader: %v", err)
	}
	grant := strings.TrimSpace(canonical.applied(t))
	for _, want := range []string{"kind: ServiceAccount", "kind: Role", "kind: RoleBinding", `resources: ["tasks"]`, `verbs: ["get"]`} {
		if !strings.Contains(grant, want) {
			t.Fatalf("the grant to compare against is missing %q:\n%s", want, grant)
		}
	}

	step := newOrkaFixture(t, installer)
	step.app.orkaInstallerDigest = digestOf(installer)
	step.app.Cfg.Model = "qwen2.5:3b"
	if err := step.app.stepOrka(); err != nil {
		t.Fatalf("step orka: %v", err)
	}
	if !strings.Contains(step.applied(t), grant) {
		t.Errorf("`kmx up --step orka` wrote a different result-reader grant:\n%s", step.applied(t))
	}

	wizard := newOrkaFixture(t, installer)
	// The wizard installs the quickstart Tool, whose grant must target the
	// chart controller's verified AI worker rather than an inferred name.
	t.Setenv("KMX_TEST_DEPLOY_JSON", `{"items":[`+workerController(orkaChartController, "orka-0.2.0", "orka-api", "Helm", `["--ai-worker-service-account-name=orka-api-ai-worker"]`)+`]}`)
	t.Setenv("KMX_TEST_WORKER_ACCOUNTS", `{"items":[`+workerAccount("orka-api-ai-worker", OrkaNamespace, "orka-0.2.0", "orka-api", "ai")+`]}`)
	t.Setenv("KMX_TEST_SAR_ALLOW", "1")
	wizard.app.orkaInstallerDigest = digestOf(installer)
	wizard.app.Cfg.Model = "qwen2.5:3b"
	if err := wizard.app.quickstartWizardOrka(func(quickstartSetupEvent) {}); err != nil {
		t.Fatalf("wizard orka: %v", err)
	}
	if !strings.Contains(wizard.applied(t), grant) {
		t.Errorf("the wizard wrote a different result-reader grant:\n%s", wizard.applied(t))
	}

	// One author in the source, not just one shape in this run: a second
	// literal elsewhere in the package would pass the comparison above on the
	// day it is written and drift on some later one.
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var authors []string
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if text := string(body); strings.Contains(text, "kind: RoleBinding") && strings.Contains(text, `resources: ["tasks"]`) {
			authors = append(authors, path)
		}
	}
	if len(authors) != 1 || authors[0] != "orka.go" {
		t.Errorf("the Task-result grant is written in %v; it belongs to orkaResultReader in orka.go alone", authors)
	}
}

// A Provider pointed somewhere other than the in-cluster default is the
// caller's own endpoint — a host Ollama reached over the kind gateway, say,
// which `kmx up` verified is reachable FROM the cluster and deployed no
// in-cluster Ollama for. Refusing it because an `ollama` Service that has
// nothing to do with it is absent would refuse a route that works.
func TestOrkaProviderChecksTheInClusterServiceOnlyForTheInClusterEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, url     string
		wantErr       bool
		wantServiceOp bool
	}{
		{"in-cluster default", orkaDefaultModelURL, true, true},
		{"host route", "http://172.18.0.1:11434/v1", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrkaFixture(t, []byte("kind: Namespace\n"))
			t.Setenv("KMX_TEST_OLLAMA", "absent")
			err := f.app.orkaProvider(orkaDefaults(OrkaOptions{ModelURL: tc.url}))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, want error=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !strings.Contains(err.Error(), "no in-cluster model server") {
					t.Fatalf("the refusal does not name the missing server: %v", err)
				}
				if applied := f.applied(t); strings.Contains(applied, "kind: Provider") {
					t.Fatalf("a Provider resolving nothing was written:\n%s", applied)
				}
				return
			}
			if strings.Contains(f.calls(t), "get svc ollama") != tc.wantServiceOp {
				t.Fatalf("looked for an unrelated in-cluster Service:\n%s", f.calls(t))
			}
			applied := f.applied(t)
			if !strings.Contains(applied, tc.url) || !strings.Contains(applied, "kind: Provider") {
				t.Fatalf("the host route was not written into the Provider:\n%s", applied)
			}
		})
	}
}
