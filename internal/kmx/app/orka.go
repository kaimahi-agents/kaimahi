package app

// `kmx orka` — the front door for a platform this project did not write.
//
// kmx verifies Orka's release chart before using its CRDs and installing the
// harness-v2 controller. The chart owns its generated snapshot encryption key;
// kmx never reads or prints that key.
//
// What this is NOT: a fork, a vendored copy, or a re-implementation. The
// chart bytes are Orka's own release asset, refused unless they hash to
// the pinned digest. Kaimahi builds
// nothing here and owns none of it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	// OrkaVersion is the tag whose installer this command was tested
	// against. It is a pin, not a floor: the digest below is of THESE
	// bytes, so a moved tag is refused rather than installed.
	OrkaVersion = "v0.2.0"

	// OrkaInstallerDigest is the published release chart asset's SHA-256.
	// v0.2.0 dereferences to 5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1.
	OrkaInstallerDigest = "b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79"

	// OrkaNamespace is kmx's Orka release and watched namespace.
	OrkaNamespace = "orka-system"

	// OrkaPathNamespaces is the guard banner's namespace list for the
	// supported Orka path: the model server and the Orka runtime, which are
	// the only two namespaces `kmx quickstart`, the wizard and a bare
	// `kmx up` write to. Other commands can write to caller-selected namespaces;
	// naming a namespace this path never touches would describe another command.
	OrkaPathNamespaces = "ollama, " + OrkaNamespace

	// Legacy v0.1.3 readiness also requires its separate wrapper Deployment.
	orkaWrapper = "orka-agent-harness-wrapper"

	// orkaProviderKind is Orka's own LLM-backend object. A Provider named
	// <p> is what makes a model called `<p>/<model>` resolvable.
	orkaProviderKind = "providers.core.orka.ai"

	// orkaModelNamespace is where `kmx up` puts the keyless model server
	// the default Provider points at.
	orkaModelNamespace = "ollama"

	// orkaDefaultProvider and orkaDefaultModelURL are the keyless Provider
	// `kmx up --step orka` wires and the in-cluster endpoint it resolves
	// against. Quickstart creates its own fixed Provider for its Agent but
	// shares this endpoint and the placeholder key with the local Provider.
	orkaDefaultProvider = "local"
	orkaDefaultModelURL = "http://ollama.ollama.svc.cluster.local:11434/v1"

	// orkaResultAccount is the read-only Task result account `kmx up --step
	// orka` provisions. Task RESULTS are read over Orka's API with a
	// short-lived token for this account, so the account has to exist before
	// any command can retrieve one. It is provisioned by the runtime step and
	// never by `kmx agent create`, which only ever NAMES an existing account:
	// minting an identity as a side effect of authoring an agent would hide a
	// grant inside a command nobody reads as a grant.
	orkaResultAccount = "orka-result-reader"
)

// OrkaInstallerURL is the pinned source of the installer.
func OrkaInstallerURL() string {
	return "https://github.com/orka-agents/orka/releases/download/" + OrkaVersion + "/orka-0.2.0.tgz"
}

// installerSource and installerDigest resolve the shipped pin unless a test
// has replaced it. Production always takes the constants.
func (a *App) installerSource() string {
	if a.orkaInstaller != "" {
		return a.orkaInstaller
	}
	return OrkaInstallerURL()
}

func (a *App) installerDigest() string {
	if a.orkaInstallerDigest != "" {
		return a.orkaInstallerDigest
	}
	return OrkaInstallerDigest
}

// OrkaOptions are `kmx orka install`'s knobs.
//
// There is deliberately no flag that could carry a credential. The wrapper
// token is generated here and never leaves the pipe into kubectl; a provider
// key is somebody else's to mint, which is why the only Provider this
// command offers to create is the keyless one.
type OrkaOptions struct {
	// Provider names the keyless Provider to create against an in-cluster
	// model server. "-" creates none.
	Provider string
	// Model is the default model that Provider resolves.
	Model string
	// ModelURL is the OpenAI-compatible endpoint the Provider points at.
	ModelURL string
	// NoApply writes nothing to the cluster: fetch, verify, and print what
	// would be applied. The one flag that reaches the network and no
	// further.
	NoApply bool
	// DryRun sends the installer to the API server for validation and
	// discards the result, which is the only way to learn that a cluster
	// would refuse it without having it half-applied.
	DryRun bool
}

// orkaDefaults fills the flags the operator left alone.
func orkaDefaults(opt OrkaOptions) OrkaOptions {
	if strings.TrimSpace(opt.Provider) == "" {
		opt.Provider = orkaDefaultProvider
	}
	if strings.TrimSpace(opt.Model) == "" {
		opt.Model = config.DefaultModel
	}
	if strings.TrimSpace(opt.ModelURL) == "" {
		opt.ModelURL = orkaDefaultModelURL
	}
	return opt
}

// OrkaInstall puts Orka on the cluster kmx is pointed at.
//
// CRDs from the verified chart are applied before Helm discovers Gateway
// schema; the chart/controller own their generated snapshot encryption key.
func (a *App) OrkaInstall(opt OrkaOptions) error {
	started := a.timeNow()
	opt = orkaDefaults(opt)
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply and --dry-run cannot be used together")
	}
	if err := a.preflight(depKubectl, depHelm); err != nil {
		return err
	}
	if opt.NoApply {
		installer, err := a.fetchOrkaInstaller()
		if err != nil {
			return err
		}
		_, count, err := orkaChartCRDs(installer)
		if err != nil {
			return err
		}
		a.notef("--no-apply: nothing was written. %d chart CRDs would precede harness-v2 in %s.", count, OrkaNamespace)
		a.notef("  Inspect after saving and SHA-256-verifying the chart: tar -tzf <verified-orka-0.2.0.tgz> | grep '^orka/crds/'")
		return nil
	}

	if err := a.Guard(fmt.Sprintf("install Orka %s (chart CRDs, harness-v2 controller) into namespace %s",
		OrkaVersion, OrkaNamespace), "kmx orka install"); err != nil {
		return err
	}

	total := 4
	switch {
	case opt.DryRun:
		total = 2
	case opt.Provider == "-":
		total = 3
	}

	var installer []byte
	if err := a.runPhase(phase{current: 1, total: total, name: "Fetch the pinned installer"}, func() error {
		var err error
		installer, err = a.fetchOrkaInstaller()
		return err
	}); err != nil {
		return err
	}

	if opt.DryRun {
		if err := a.runPhase(phase{current: 2, total: total, name: "Validate chart CRDs"}, func() error {
			if _, err := a.orkaInstallState(); err != nil {
				return err
			}
			crds, _, err := orkaChartCRDs(installer)
			if err != nil {
				return err
			}
			quiet := *a.Run
			quiet.Echo = false
			quiet.Stdout, quiet.Stderr = io.Discard, io.Discard
			return quiet.RunStdin(crds, "kubectl", a.kubectl("apply", "--dry-run=server", "-f", "-")...)
		}); err != nil {
			return fmt.Errorf("this cluster would refuse Orka's installer: %w", err)
		}
		a.complete("Validated; nothing was written", started)
		a.notef("A server dry run cannot show whether chart Secrets or the controller become ready; only a real install can.")
		return nil
	}

	if err := a.runPhase(phase{current: 2, total: total, name: "Apply chart CRDs and install Orka"}, func() error {
		return a.applyOrkaInstaller(installer)
	}); err != nil {
		return err
	}

	if err := a.runPhase(phase{current: 3, total: total, name: "Provision Task result reader"}, a.orkaResultReader); err != nil {
		return err
	}

	if opt.Provider != "-" {
		if err := a.runPhase(phase{current: 4, total: total, name: "Wire a keyless Provider"}, func() error {
			return a.orkaProvider(opt)
		}); err != nil {
			return err
		}
	}

	a.complete("Orka "+OrkaVersion+" is running", started)
	a.notef("  kmx orka status       # what is installed, and what it can resolve")
	return nil
}

// stepOrka is `kmx up --step orka`: the pinned runtime, its keyless Provider
// and the account a Task result is read with.
//
// It is `kmx orka install`'s work without `kmx orka install`'s framing — the
// same fetch, digest refusal, and chart-CRDs-before-Helm order
// — because `up` supplies its own phase, guard and completion lines. Calling
// OrkaInstall here would nest a four-phase command inside one phase of
// another and end it with a second "COMPLETE".
func (a *App) stepOrka() error {
	opt := orkaDefaults(OrkaOptions{Model: a.Cfg.Model})
	// A verified host model is already reachable FROM the cluster (that is
	// what verifySelectedLocalModel proves), and it is the model this run
	// deployed no in-cluster Ollama for. Pointing the Provider at the
	// in-cluster address instead would wire a Provider to nothing.
	if a.selectedLocalModel != nil {
		opt.Model = a.selectedLocalModel.Model
		opt.ModelURL = strings.TrimSuffix(a.selectedLocalModel.Endpoint, "/") + "/v1"
	}
	if err := a.prepareOrkaRuntime(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.operationContext(), 5*time.Minute)
	defer cancel()
	current, err := a.readStepOrkaProvider(ctx, opt.Provider)
	if err != nil {
		return err
	}
	if current != nil && current.Endpoint != opt.ModelURL {
		return fmt.Errorf("Provider %s/%s has a different endpoint:\n  - existing: %s\n  + requested: %s\n  Refusing to overwrite it. The following command explicitly replaces the host route:\n  %s",
			OrkaNamespace, opt.Provider, current.Endpoint, opt.ModelURL,
			a.operationCommand("orka", "install", "--provider", opt.Provider, "--model", opt.Model, "--model-url", opt.ModelURL))
	}
	if err := a.orkaProvider(opt); err != nil {
		return err
	}
	applied, err := a.readStepOrkaProvider(ctx, opt.Provider)
	if err != nil {
		return err
	}
	if applied == nil {
		return fmt.Errorf("Provider %s/%s is absent after apply; cannot prove it is Ready", OrkaNamespace, opt.Provider)
	}
	if err := a.waitOrkaReady(ctx, OrkaNamespace, applied.orkaIdentity); err != nil {
		return err
	}
	return a.orkaResultReader()
}

// Quickstart needs the pinned runtime and placeholder Provider Secret, not the local
// Provider: its fixed agent bundle creates a separate Provider of its own.
func (a *App) stepQuickstartOrka() error {
	if err := a.prepareOrkaRuntime(); err != nil {
		return err
	}
	if err := a.ensureQuickstartProviderSecret(); err != nil {
		return err
	}
	return a.orkaResultReader()
}

func (a *App) prepareOrkaRuntime() error {
	installer, err := a.fetchOrkaInstaller()
	if err != nil {
		return err
	}
	return a.applyOrkaInstaller(installer)
}

// readStepOrkaProvider distinguishes absence from an unreadable or malformed
// Provider. After apply, its returned UID/generation pins the Ready wait.
func (a *App) readStepOrkaProvider(ctx context.Context, name string) (*stepOrkaProvider, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", orkaProviderKind, name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("cannot read Provider %s/%s: %w", OrkaNamespace, name, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var obj struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name              string     `json:"name"`
			Namespace         string     `json:"namespace"`
			UID               string     `json:"uid"`
			Generation        int64      `json:"generation"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp"`
		} `json:"metadata"`
		Spec struct {
			BaseURL string `json:"baseURL"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Kind != "Provider" || obj.Metadata.Name != name || obj.Metadata.Namespace != OrkaNamespace || obj.Metadata.UID == "" || obj.Metadata.Generation < 1 || obj.Metadata.DeletionTimestamp != nil || obj.Spec.BaseURL == "" {
		return nil, fmt.Errorf("Provider %s/%s has an invalid or terminating identity or endpoint; refusing to overwrite it", OrkaNamespace, name)
	}
	return &stepOrkaProvider{orkaIdentity: orkaIdentity{Kind: "Provider", Name: name, UID: obj.Metadata.UID, Generation: obj.Metadata.Generation}, Endpoint: obj.Spec.BaseURL}, nil
}

type stepOrkaProvider struct {
	orkaIdentity
	Endpoint string
}

// orkaResultReader provisions the read-only Task result account.
//
// Its authority is one verb on one resource in one namespace, written out
// here rather than pointed at a helper, because this is a GRANT and the thing
// worth reviewing is its exact extent. Orka v0.1.3 authenticates result reads
// but does not enforce Task-read RBAC, so this Role is the ceiling kmx can
// state, not a guarantee the server enforces — which is why it is kept this
// small.
func (a *App) orkaResultReader() error {
	manifest := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: ` + orkaResultAccount + `
  namespace: ` + OrkaNamespace + `
  labels:
    app.kubernetes.io/managed-by: kmx
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ` + orkaResultAccount + `
  namespace: ` + OrkaNamespace + `
  labels:
    app.kubernetes.io/managed-by: kmx
rules:
  - apiGroups: ["core.orka.ai"]
    resources: ["tasks"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ` + orkaResultAccount + `
  namespace: ` + OrkaNamespace + `
  labels:
    app.kubernetes.io/managed-by: kmx
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ` + orkaResultAccount + `
subjects:
  - kind: ServiceAccount
    name: ` + orkaResultAccount + `
    namespace: ` + OrkaNamespace + `
`
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (ServiceAccount/Role/RoleBinding %s)\n",
		a.Cfg.KubeContext, orkaResultAccount)
	if err := quiet.RunStdin([]byte(manifest), "kubectl", a.kubectl("apply", "-f", "-")...); err != nil {
		return fmt.Errorf("provisioning the Task result account %s/%s: %w", OrkaNamespace, orkaResultAccount, err)
	}
	a.notef("ServiceAccount %s may get Tasks in %s and nothing else. A result token carries\n"+
		"  its full effective authority, so it is kept to that one verb.", orkaResultAccount, OrkaNamespace)
	return nil
}

// fetchOrkaInstaller downloads the published release chart and refuses any
// bytes other than the release asset's pinned digest.
func (a *App) fetchOrkaInstaller() ([]byte, error) {
	url := a.installerSource()
	fmt.Fprintf(a.Err, "curl -fsSL %s # (sha256-pinned)\n", url)

	client := &http.Client{Timeout: 60 * time.Second}
	request, err := http.NewRequestWithContext(a.operationContext(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching Orka's installer from %s: %w\n"+
			"  Nothing was applied. This command needs the internet once, to read their tag", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Orka's installer from %s: HTTP %d\n"+
			"  Nothing was applied", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading Orka's installer: %w", err)
	}

	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != a.installerDigest() {
		return nil, fmt.Errorf("Orka's installer at %s does not hash to the digest kmx pins.\n"+
			"  expected %s\n  got      %s\n"+
			"  Nothing was applied. Either the tag moved or the bytes were changed in transit;\n"+
			"  neither is something to install past. `kmx orka install` pins one tested version.",
			OrkaVersion, a.installerDigest(), got)
	}
	a.notef("Installer verified: %d kB, sha256 %s.", len(body)/1024, got[:12])
	return body, nil
}

// applyOrkaInstaller installs the verified chart without exposing its
// controller-generated key. Keeping this shared entry point means standalone
// install, up, quickstart, the wizard and AKS take the same guarded path.

func (a *App) applyOrkaInstaller(installer []byte) error { return a.applyOrkaChart(installer) }

// orkaProvider creates a Provider pointing at an in-cluster, keyless model
// server, which is the step that removes Orka's fourth prerequisite.
//
// Orka's Provider requires a secretRef even where the endpoint needs no key,
// so a placeholder is written to satisfy the schema. It is named for what it
// is rather than dressed up as a credential.
func (a *App) orkaProvider(opt OrkaOptions) error {
	if err := scaffold.ValidateName(opt.Provider); err != nil {
		return fmt.Errorf("--provider %q: %w", opt.Provider, err)
	}
	// Only the in-cluster default is checkable from here. A `--model-url`
	// pointing somewhere else (a host Ollama reached over the kind gateway,
	// say) is the caller's own endpoint, and looking for an `ollama` Service
	// that has nothing to do with it would refuse a route that works.
	if opt.ModelURL == orkaDefaultModelURL {
		if _, err := a.kubectlCapture("-n", orkaModelNamespace, "get", "svc", "ollama", "-o", "name"); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("no in-cluster model server at %s, so Provider %q would resolve nothing.\n"+
					"  `%s` deploys one on the selected local kind target. To install Orka without a Provider: --provider -",
					opt.ModelURL, opt.Provider, a.operationCommand("local", "up"))
			}
			return fmt.Errorf("cannot tell whether an in-cluster model server exists (refusing to guess): %w", err)
		}
	}

	if err := a.orkaKeylessProviderSecret(opt.Provider); err != nil {
		return err
	}
	secret := opt.Provider + "-provider-key"
	provider := fmt.Sprintf(`apiVersion: core.orka.ai/v1alpha1
kind: Provider
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
spec:
  type: openai
  baseURL: %s
  defaultModel: %s
  secretRef:
    name: %s
    key: api-key
`, opt.Provider, OrkaNamespace, opt.ModelURL, opt.Model, secret)

	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (Provider %s -> %s)\n",
		a.Cfg.KubeContext, opt.Provider, opt.ModelURL)
	if err := quiet.RunStdin([]byte(provider), "kubectl", a.kubectl("apply", "-f", "-")...); err != nil {
		return err
	}
	a.notef("Provider %q resolves %s/%s against %s — no API key anywhere.",
		opt.Provider, opt.Provider, opt.Model, opt.ModelURL)
	return nil
}

// Reuse an existing Secret without replacing a host route's key. Only an
// absent Secret is bootstrapped; a present Secret lacking the key is not ours
// to repair by overwriting it.
func (a *App) ensureQuickstartProviderSecret() error {
	ctx, cancel := context.WithTimeout(a.operationContext(), 30*time.Second)
	defer cancel()
	secret := orkaDefaultProvider + "-provider-key"
	marker, err := a.orkaCapture(ctx, nil, "-n", OrkaNamespace, "get", "secret", secret,
		"--ignore-not-found=true", "-o", "go-template=secret\n{{range $key, $_ := .data}}{{if eq $key \"api-key\"}}present{{end}}{{end}}")
	if err != nil {
		return fmt.Errorf("cannot inspect quickstart Provider Secret: %w", err)
	}
	switch string(marker) {
	case "":
		return a.orkaKeylessProviderSecret(orkaDefaultProvider)
	case "secret\npresent":
		return nil
	case "secret\n":
		return fmt.Errorf("Provider Secret %s/%s exists without api-key; refusing to replace it", OrkaNamespace, secret)
	default:
		return fmt.Errorf("Provider Secret %s/%s returned an invalid key-presence marker", OrkaNamespace, secret)
	}
}

func (a *App) orkaKeylessProviderSecret(provider string) error {
	secret := provider + "-provider-key"
	body := secretManifest(secret, OrkaNamespace, map[string]string{"api-key": "not-used-by-this-endpoint"},
		map[string]string{"app.kubernetes.io/managed-by": "kmx"})
	return a.applySecretIn(OrkaNamespace, body, secret)
}

// unreachable conservatively distinguishes a missing object from an API
// server that could not answer. Unknown errors remain real errors.
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sign := range []string{
		"connection refused", "could not be reached", "unable to connect to the server",
		"couldn't get current server api group list", "no such host", "i/o timeout",
		"connection timed out", "tls handshake timeout", "context does not exist",
		"no configuration has been", "invalid configuration",
		"server has asked for the client to provide credentials",
	} {
		if strings.Contains(msg, sign) {
			return true
		}
	}
	return false
}

// OrkaReady verifies the selected controller's current rollout; legacy
// installations additionally require their wrapper to be ready.
//
// It exists because `OrkaStatus` is a VIEW: it prints "not installed" and
// returns nil, which is right for a human asking a question and wrong for a
// caller deciding whether a lift succeeded. A verification step that consulted
// the view would report "installed and ready" about a cluster with no Orka on
// it at all.
//
// Unreadable is its own answer, and not a pass: a cluster that did not respond
// has not been shown to be ready.
func (a *App) OrkaReady() error {
	deployments, err := a.orkaDeployments(a.operationContext())
	if err != nil {
		return fmt.Errorf("cannot read Orka in namespace %s, so it has NOT been shown to be ready: %w", OrkaNamespace, err)
	}
	controller, legacy, err := selectOrkaController(deployments)
	if err != nil {
		return fmt.Errorf("nothing was verified: %w", err)
	}
	required := []orkaDeployment{controller}
	if legacy {
		found := false
		for _, d := range deployments {
			if d.Metadata.Name == orkaWrapper {
				required = append(required, d)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Orka's %s is not on this cluster in namespace %s: nothing was verified", orkaWrapper, OrkaNamespace)
		}
	}
	for _, d := range required {
		name := d.Metadata.Name
		// Ready replicas alone are not a finished rollout. A Deployment whose
		// new pod is in ImagePullBackOff still reports the OLD pod as ready,
		// so `1/1` would pass a rollout that never landed. The generation and
		// the updated/available counts are what distinguish "this spec is
		// running" from "some spec is running".
		switch {
		case d.Spec.Replicas <= 0:
			return fmt.Errorf("Orka's %s is scaled to %d in namespace %s, so nothing is running to verify.\n"+
				"  Scale it up, or reinstall with `kmx orka install`",
				name, d.Spec.Replicas, OrkaNamespace)
		case d.Status.ObservedGeneration < d.Metadata.Generation:
			return fmt.Errorf("Orka's %s has a spec its controller has not observed yet in namespace %s "+
				"(generation %d, observed %d), so this lift is not complete.\n"+
				"  Watch it finish:  kubectl -n %s rollout status deploy/%s",
				name, OrkaNamespace, d.Metadata.Generation, d.Status.ObservedGeneration, OrkaNamespace, name)
		case d.Status.UpdatedReplicas != d.Spec.Replicas,
			d.Status.AvailableReplicas != d.Spec.Replicas,
			d.Status.ReadyReplicas != d.Spec.Replicas,
			d.Status.UnavailableReplicas != 0:
			// List the namespace without guessing a deployment-specific label:
			// Orka labels every pod `app.kubernetes.io/name=orka`.
			return fmt.Errorf("Orka's %s has not finished rolling out in namespace %s: "+
				"%d/%d updated, %d ready, %d available, %d unavailable.\n"+
				"  A ready count alone can be the OLD pod while its replacement fails to start.\n"+
				"  Its rollout says why:  kubectl -n %s rollout status deploy/%s\n"+
				"  And the pods:          kubectl -n %s get pods",
				name, OrkaNamespace, d.Status.UpdatedReplicas, d.Spec.Replicas, d.Status.ReadyReplicas,
				d.Status.AvailableReplicas, d.Status.UnavailableReplicas, OrkaNamespace, name, OrkaNamespace)
		}
	}
	return nil
}

// OrkaStatus reports what is installed and what it can resolve.
//
// Three facts, separately, because any one can be true while the others are
// not — and an unreadable cluster is reported as unread rather than as
// absent.
func (a *App) OrkaStatus() error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	deployments, err := a.kubectlCapture("-n", OrkaNamespace, "get", "deploy",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.status.readyReplicas}/{.spec.replicas} {end}")
	if unreachable(err) {
		return fmt.Errorf("cannot read Orka: the cluster did not answer.\n" +
			"  This is not the same as Orka being absent — kmx does not know either way. Check the selected context and API server")
	}
	if err != nil && !isNotFound(err) {
		if strings.Contains(strings.ToLower(err.Error()), "forbidden") || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			return fmt.Errorf("cannot read Orka Deployments in %s: access forbidden; check the selected context and permissions", OrkaNamespace)
		}
		return fmt.Errorf("cannot read Orka Deployments in %s: kubectl request failed; check the selected context and permissions", OrkaNamespace)
	}
	if isNotFound(err) || strings.TrimSpace(deployments) == "" {
		fmt.Fprintf(a.Out, "%-22s %s\n", "orka", "not installed (`kmx orka install`)")
		return nil
	}

	version, err := a.orkaRunningVersion()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%-22s %s\n", "version running", version)
	fmt.Fprintf(a.Out, "%-22s %s\n", "version pinned by kmx", OrkaVersion)
	fmt.Fprintf(a.Out, "%-22s %s\n", "deployments", strings.TrimSpace(deployments))

	crds, err := a.kubectlCapture("get", "crd", "-o",
		"jsonpath={range .items[?(@.spec.group=='core.orka.ai')]}{.metadata.name} {end}")
	if err == nil {
		fmt.Fprintf(a.Out, "%-22s %d in core.orka.ai\n", "crds", len(strings.Fields(crds)))
	}

	providers, err := a.kubectlCapture("-n", OrkaNamespace, "get", orkaProviderKind,
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.status.ready}{\" \"}{end}")
	switch {
	case err != nil && !isNotFound(err):
		fmt.Fprintf(a.Out, "%-22s %s\n", "providers", "unreadable")
	case strings.TrimSpace(providers) == "":
		fmt.Fprintf(a.Out, "%-22s %s\n", "providers", "none — a model call would be refused")
	default:
		fmt.Fprintf(a.Out, "%-22s %s\n", "providers", strings.TrimSpace(providers))
	}

	return nil
}
