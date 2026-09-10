package app

// `kmx orka` — the front door for a platform this project did not write.
//
// Orka assumes a cluster. It publishes no installer and no CLI binary: its
// documented path is `kubectl apply -f deploy/orka.yaml` from a checkout,
// preceded by a Secret the operator is told to create by hand because "raw
// manifests cannot safely contain a shared bearer token". Skip that step and
// the install still succeeds — the wrapper Deployment simply never becomes
// ready, which is the shape of failure this command exists to remove.
//
// What this is NOT: a fork, a vendored copy, or a re-implementation. The
// bytes applied are Orka's own, fetched from their tag and refused unless
// they hash to the digest this command was tested against. Kaimahi builds
// nothing here and owns none of it.
//
// The governance seam is a separate decision and stays one: `kmx migrate`
// puts an application's model traffic through the plane on the way to Orka.
// Installing Orka governs nothing by itself, and this command says so.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const (
	// OrkaVersion is the tag whose installer this command was tested
	// against. It is a pin, not a floor: the digest below is of THESE
	// bytes, so a moved tag is refused rather than installed.
	OrkaVersion = "v0.1.3"

	// OrkaInstallerDigest is the sha256 of deploy/orka.yaml at OrkaVersion.
	// Orka publishes no GitHub Releases and no checksum file, so there is
	// nothing upstream to verify against — this digest is ours, recorded
	// from the bytes that were read and installed here. That makes it a
	// weaker claim than a publisher's signature and a stronger one than
	// trusting whatever the URL serves today, which is the honest middle
	// and the reason it is written down rather than skipped.
	OrkaInstallerDigest = "33bdd38bc4aff5d9ef0c32cd5a6c2810a186d2c3ab482b5fdc0f0673a5d734cd"

	// OrkaNamespace is the namespace their installer creates and pins its
	// own objects to. It is not configurable here because it is not
	// configurable there: the manifest hard-codes it in 77 documents.
	OrkaNamespace = "orka-system"

	// orkaWrapperSecret is the Secret their getting-started tells an
	// operator to create with `openssl rand -hex 32` before applying.
	orkaWrapperSecret = "harness-wrapper-auth"

	// The two Deployments the installer creates. Readiness of both is what
	// "installed" means; anything less is "submitted".
	orkaController = "orka-controller-manager"
	orkaWrapper    = "orka-agent-harness-wrapper"

	// orkaProviderKind is Orka's own LLM-backend object. A Provider named
	// <p> is what makes a model called `<p>/<model>` resolvable, which is
	// the contract `kmx migrate --model` is checked against.
	orkaProviderKind = "providers.core.orka.ai"

	// orkaModelNamespace is where `kmx up` puts the keyless model server
	// the default Provider points at.
	orkaModelNamespace = "ollama"
)

// OrkaInstallerURL is the pinned source of the installer.
func OrkaInstallerURL() string {
	return "https://raw.githubusercontent.com/orka-agents/orka/" + OrkaVersion + "/deploy/orka.yaml"
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
}

// orkaDefaults fills the flags the operator left alone.
func orkaDefaults(opt OrkaOptions) OrkaOptions {
	if strings.TrimSpace(opt.Provider) == "" {
		opt.Provider = "local"
	}
	if strings.TrimSpace(opt.Model) == "" {
		opt.Model = "qwen2.5:3b"
	}
	if strings.TrimSpace(opt.ModelURL) == "" {
		opt.ModelURL = "http://ollama.ollama.svc.cluster.local:11434/v1"
	}
	return opt
}

// OrkaInstall puts Orka on the cluster kmx is pointed at.
//
// The order is the one their own documentation gives, and the middle step is
// the reason this command exists: the Secret must exist BEFORE the manifest,
// because the manifest cannot carry it and the wrapper mounts it at start.
func (a *App) OrkaInstall(opt OrkaOptions) error {
	started := a.timeNow()
	opt = orkaDefaults(opt)
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if err := a.Guard(fmt.Sprintf("install Orka %s (17 CRDs, 2 Deployments) into namespace %s",
		OrkaVersion, OrkaNamespace), "kmx orka install"); err != nil {
		return err
	}

	total := 4
	if opt.Provider == "-" {
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

	if err := a.runPhase(phase{current: 2, total: total, name: "Reconcile the wrapper credential"}, func() error {
		return a.orkaWrapperCredential()
	}); err != nil {
		return err
	}

	if err := a.runPhase(phase{current: 3, total: total, name: "Apply the installer and wait"}, func() error {
		return a.applyOrkaInstaller(installer)
	}); err != nil {
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
	a.notef("\nNOTE  Installing Orka governs nothing by itself. An application's model\n" +
		"      traffic reaches it directly until it is put on the seam:")
	if opt.Provider != "-" {
		a.notef("  kmx migrate <deployment> --namespace <ns> --model %s/%s", opt.Provider, opt.Model)
	} else {
		a.notef("  kmx migrate <deployment> --namespace <ns> --model <provider>/<model>")
	}
	a.notef("  kmx orka status       # what is installed, and what it can resolve")
	return nil
}

// fetchOrkaInstaller downloads the pinned manifest and refuses anything
// whose bytes are not the ones this command was tested against.
//
// Fetched rather than vendored: 525 kB of somebody else's manifest committed
// here would be a copy that silently ages, and the repository map would have
// to call it Product. Fetched-and-pinned keeps the bytes theirs and the
// decision to install exactly these bytes ours.
func (a *App) fetchOrkaInstaller() ([]byte, error) {
	url := a.installerSource()
	fmt.Fprintf(a.Err, "curl -fsSL %s # (sha256-pinned)\n", url)

	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Get(url)
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

// orkaWrapperCredential creates the namespace and the shared bearer token
// the harness wrapper reads, and never replaces one that is already there.
//
// Regenerating it under a running wrapper would rotate a secret its own
// callers still hold, so an existing value is kept — the same rule the
// plane's own secrets follow.
func (a *App) orkaWrapperCredential() error {
	if err := a.kubectlRun("create", "namespace", OrkaNamespace,
		"--dry-run=client", "-o", "yaml"); err != nil {
		return err
	}
	namespace := []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + OrkaNamespace + "\n")
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (namespace %s)\n", a.Cfg.KubeContext, OrkaNamespace)
	if err := quiet.RunStdin(namespace, "kubectl", a.kubectl("apply", "-f", "-")...); err != nil {
		return err
	}

	_, err := a.kubectlCapture("-n", OrkaNamespace, "get", "secret", orkaWrapperSecret, "-o", "name")
	switch {
	case err == nil:
		a.notef("Secret %s exists; keeping it.", orkaWrapperSecret)
		return nil
	case !isNotFound(err):
		// An unreachable API server answered as "absent" would mint a
		// second token under a running wrapper, which is the one outcome
		// worse than stopping.
		return fmt.Errorf("cannot tell whether Secret %s exists in %s (refusing to generate a second one): %w",
			orkaWrapperSecret, OrkaNamespace, err)
	}

	token, err := randomHex(32)
	if err != nil {
		return err
	}
	body := secretManifest(orkaWrapperSecret, OrkaNamespace, map[string]string{"token": token},
		map[string]string{"app.kubernetes.io/managed-by": "kmx"})
	if err := a.applySecretIn(OrkaNamespace, body, orkaWrapperSecret); err != nil {
		return err
	}
	a.notef("Secret %s created. Orka's own instructions ask an operator to do this by hand\n"+
		"  with `openssl rand -hex 32`; an install that skips it comes up and never becomes ready.",
		orkaWrapperSecret)
	return nil
}

// applyOrkaInstaller applies their manifest unmodified and then waits for
// both Deployments, because "applied" is not "running".
func (a *App) applyOrkaInstaller(installer []byte) error {
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (Orka %s, %d documents)\n",
		a.Cfg.KubeContext, OrkaVersion, strings.Count(string(installer), "\n---\n")+1)
	if err := quiet.RunStdin(installer, "kubectl", a.kubectl("apply", "-f", "-")...); err != nil {
		return fmt.Errorf("applying Orka's installer: %w", err)
	}
	for _, deployment := range []string{orkaController, orkaWrapper} {
		if err := a.kubectlRun("-n", OrkaNamespace, "rollout", "status",
			"deploy/"+deployment, "--timeout=300s"); err != nil {
			return fmt.Errorf("Orka's %s did not become ready: %w\n"+
				"  Its logs say why:  kubectl -n %s logs deploy/%s",
				deployment, err, OrkaNamespace, deployment)
		}
	}
	return nil
}

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
	if _, err := a.kubectlCapture("-n", orkaModelNamespace, "get", "svc", "ollama", "-o", "name"); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("no in-cluster model server at %s, so Provider %q would resolve nothing.\n"+
				"  `kmx up` deploys one. To install Orka without a Provider: --provider -",
				opt.ModelURL, opt.Provider)
		}
		return fmt.Errorf("cannot tell whether an in-cluster model server exists (refusing to guess): %w", err)
	}

	secret := opt.Provider + "-provider-key"
	body := secretManifest(secret, OrkaNamespace, map[string]string{"api-key": "not-used-by-this-endpoint"},
		map[string]string{"app.kubernetes.io/managed-by": "kmx"})
	if err := a.applySecretIn(OrkaNamespace, body, secret); err != nil {
		return err
	}

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

// OrkaStatus reports what is installed and what it can resolve.
//
// Three facts, separately, because any one can be true while the others are
// not — and an unreadable cluster is reported as unread rather than as
// absent, which is the rule `kmx tools sandbox status` already follows.
func (a *App) OrkaStatus() error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	deployments, err := a.kubectlCapture("-n", OrkaNamespace, "get", "deploy",
		"-o", "jsonpath={range .items[*]}{.metadata.name}={.status.readyReplicas}/{.spec.replicas} {end}")
	if unreachable(err) {
		return fmt.Errorf("cannot read Orka: the cluster did not answer.\n"+
			"  This is not the same as Orka being absent — kmx does not know either way.\n  %w", err)
	}
	if isNotFound(err) || strings.TrimSpace(deployments) == "" {
		fmt.Fprintf(a.Out, "%-22s %s\n", "orka", "not installed (`kmx orka install`)")
		return nil
	}

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

	fmt.Fprintf(a.Out, "\n%s\n", "Governed = whether an application's model traffic goes through the plane.")
	fmt.Fprintf(a.Out, "%s\n", "Installing Orka does not do that; `kmx migrate` does, one workload at a time.")
	return nil
}
