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

	// OrkaPathNamespaces is the guard banner's namespace list for the
	// supported Orka path: the model server and the Orka runtime, which are
	// the only two namespaces `kmx quickstart`, the wizard and a bare
	// `kmx up` write to. config.GuardNamespaces is the wider list used by
	// commands whose target namespaces vary or include the plane — a banner
	// naming a namespace this path never touches would describe somebody
	// else's command.
	OrkaPathNamespaces = "ollama, " + OrkaNamespace

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

	// orkaDefaultProvider and orkaDefaultModelURL are the keyless Provider
	// `kmx up --step orka` wires and the in-cluster endpoint it resolves
	// against. They are named rather than repeated because `kmx quickstart`
	// builds an Agent against the SAME Provider: a second spelling of either
	// would produce an Agent pointing at a Provider that does not exist.
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

// orkaRunningVersion reads the tag off the controller's own image.
//
// The pin is what kmx WOULD install; it is not evidence about what is
// running. An Orka installed by their Helm chart, by `kubectl apply` from a
// checkout, or by an older kmx is a different version, and a status command
// that restated the pin would report a version nobody had installed — which
// is the precise failure this command exists to avoid making.
//
// An image kmx cannot parse is reported as unread rather than guessed at.
func (a *App) orkaRunningVersion() string {
	image, err := a.kubectlCapture("-n", OrkaNamespace, "get", "deploy", orkaController,
		"-o", "jsonpath={.spec.template.spec.containers[0].image}")
	if err != nil {
		return "unknown (the controller's image could not be read)"
	}
	image = strings.TrimSpace(image)
	_, tag, found := strings.Cut(image, ":")
	if !found || tag == "" {
		return "unknown (" + image + " names no tag)"
	}
	// Their manifest tags the image `0.1.3`; the repository tags the release
	// `v0.1.3`. Compare the numbers rather than the spelling, so a match is
	// not reported as a difference.
	if strings.TrimPrefix(tag, "v") == strings.TrimPrefix(OrkaVersion, "v") {
		return tag
	}
	return tag + " (kmx pins " + OrkaVersion + " — this cluster was installed another way)"
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
// The order is the one their own documentation gives, and the middle step is
// the reason this command exists: the Secret must exist BEFORE the manifest,
// because the manifest cannot carry it and the wrapper mounts it at start.
func (a *App) OrkaInstall(opt OrkaOptions) error {
	started := a.timeNow()
	opt = orkaDefaults(opt)
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply and --dry-run cannot be used together")
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if opt.NoApply {
		installer, err := a.fetchOrkaInstaller()
		if err != nil {
			return err
		}
		a.notef("--no-apply: nothing was written. %d documents would be applied to namespace %s,\n"+
			"  after the %s Secret, which is created first because the wrapper mounts it at start.",
			orkaDocuments(installer), OrkaNamespace, orkaWrapperSecret)
		a.notef("  See them:  curl -fsSL %s", a.installerSource())
		return nil
	}

	if err := a.Guard(fmt.Sprintf("install Orka %s (17 CRDs, 2 Deployments) into namespace %s",
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

	// A server-side dry run is the only way to learn that this cluster would
	// refuse the installer — a Pod Security policy on the namespace, an API
	// server without ValidatingAdmissionPolicy — without finding out halfway
	// through applying it. It writes nothing, so it skips the Secret too.
	if opt.DryRun {
		if err := a.runPhase(phase{current: 2, total: total, name: "Validate against the API server"}, func() error {
			quiet := *a.Run
			quiet.Echo = false
			fmt.Fprintf(a.Err, "kubectl --context %s apply --dry-run=server -f - # (Orka %s)\n",
				a.Cfg.KubeContext, OrkaVersion)
			return quiet.RunStdin(installer, "kubectl",
				a.kubectl("apply", "--dry-run=server", "-f", "-")...)
		}); err != nil {
			return fmt.Errorf("this cluster would refuse Orka's installer: %w", err)
		}
		a.complete("Validated; nothing was written", started)
		a.notef("\nNOTE  A server dry run does not create the %s Secret, so it cannot show\n"+
			"      whether the wrapper would become ready. Only a real install does that.",
			orkaWrapperSecret)
		return nil
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

// stepOrka is `kmx up --step orka`: the pinned runtime, its keyless Provider
// and the account a Task result is read with.
//
// It is `kmx orka install`'s work without `kmx orka install`'s framing — the
// same fetch, the same digest refusal, the same Secret-before-manifest order
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
	installer, err := a.fetchOrkaInstaller()
	if err != nil {
		return err
	}
	if err := a.orkaWrapperCredential(); err != nil {
		return err
	}
	if err := a.applyOrkaInstaller(installer); err != nil {
		return err
	}
	if err := a.orkaProvider(opt); err != nil {
		return err
	}
	return a.orkaResultReader()
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
// orkaDocuments counts the YAML documents in their installer, so a note can
// say how much is about to arrive.
func orkaDocuments(installer []byte) int {
	return strings.Count(string(installer), "\n---\n") + 1
}

func (a *App) applyOrkaInstaller(installer []byte) error {
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (Orka %s, %d documents)\n",
		a.Cfg.KubeContext, OrkaVersion, orkaDocuments(installer))
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
	// Only the in-cluster default is checkable from here. A `--model-url`
	// pointing somewhere else (a host Ollama reached over the kind gateway,
	// say) is the caller's own endpoint, and looking for an `ollama` Service
	// that has nothing to do with it would refuse a route that works.
	if opt.ModelURL == orkaDefaultModelURL {
		if _, err := a.kubectlCapture("-n", orkaModelNamespace, "get", "svc", "ollama", "-o", "name"); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("no in-cluster model server at %s, so Provider %q would resolve nothing.\n"+
					"  `kmx up` deploys one. To install Orka without a Provider: --provider -",
					opt.ModelURL, opt.Provider)
			}
			return fmt.Errorf("cannot tell whether an in-cluster model server exists (refusing to guess): %w", err)
		}
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

// OrkaReady answers, strictly, whether Orka is installed AND both of its
// Deployments have every replica ready.
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
	raw, err := a.kubectlCapture("-n", OrkaNamespace, "get", "deploy", "-o", "json")
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("cannot read Orka in namespace %s, so it has NOT been shown to be ready: %w",
			OrkaNamespace, err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name       string `json:"name"`
				Generation int64  `json:"generation"`
			} `json:"metadata"`
			Spec struct {
				Replicas int32 `json:"replicas"`
			} `json:"spec"`
			Status struct {
				ObservedGeneration  int64 `json:"observedGeneration"`
				UpdatedReplicas     int32 `json:"updatedReplicas"`
				ReadyReplicas       int32 `json:"readyReplicas"`
				AvailableReplicas   int32 `json:"availableReplicas"`
				UnavailableReplicas int32 `json:"unavailableReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return fmt.Errorf("cannot read Orka's deployments in namespace %s, so it has NOT been shown to be ready: %w",
				OrkaNamespace, err)
		}
	}
	found := map[string]int{}
	for i, item := range list.Items {
		found[item.Metadata.Name] = i
	}
	for _, name := range []string{orkaController, orkaWrapper} {
		i, present := found[name]
		if !present {
			return fmt.Errorf("Orka's %s is not on this cluster in namespace %s: "+
				"nothing was verified, because there is nothing there.\n"+
				"  Install it with `kmx orka install`, or resume this lift at its orka phase",
				name, OrkaNamespace)
		}
		d := list.Items[i]
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
			// No guessed label selector here. An earlier version suggested
			// `-l app.kubernetes.io/name=<deployment>` and that matches
			// nothing: Orka labels every pod `app.kubernetes.io/name=orka`,
			// so the hint printed "No resources found" at the exact moment
			// the operator needed it. The namespace is Orka's own and holds
			// few pods, so listing it needs no selector to be right.
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
		return fmt.Errorf("cannot read Orka: the cluster did not answer.\n"+
			"  This is not the same as Orka being absent — kmx does not know either way.\n  %w", err)
	}
	if isNotFound(err) || strings.TrimSpace(deployments) == "" {
		fmt.Fprintf(a.Out, "%-22s %s\n", "orka", "not installed (`kmx orka install`)")
		return nil
	}

	fmt.Fprintf(a.Out, "%-22s %s\n", "version running", a.orkaRunningVersion())
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
