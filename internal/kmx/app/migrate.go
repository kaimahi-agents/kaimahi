package app

// `kmx migrate` — putting an application this project did not write onto
// Orka, with its model traffic authenticated, scoped and recorded, and
// without changing the application.
//
// What "without changing the application" means precisely, because it is
// the whole claim: no source edit, no rebuilt image, no fork of somebody's
// chart. What does change is four environment variables and one mounted
// file on a Deployment the adopter owns — and kmx writes that patch to a
// file rather than applying it, for the same reason `kmx tools sidecar`
// does: kmx may create objects it owns in a namespace it was named, and
// must not silently mutate somebody else's workload.
//
// Two obstacles are handled here rather than worked around, and both were
// measured before they were coded:
//
//  1. The framework speaks the Responses API and Orka's compatible
//     endpoint has no Responses route — an unrouted path there returns
//     the dashboard's HTML under a 200, which is worse than a 404 because
//     the client crashes three frames deep on HTML it cannot parse. The
//     seam accepts the framework's path and translates it (the `orka`
//     entry's client_path in k8s/plane/upstreams.yaml).
//
//  2. Orka's compatible endpoint injects its own coordinator prompt and
//     tool definitions by default, and the only way off that is the
//     per-request header X-Orka-Tools: disabled — which an application
//     configured by environment variables cannot send. The seam sends it.
//
// And one thing is NOT handled here: the tool seam. Governing an
// application's tool calls is a separate decision with its own commands
// (`kmx tools add`, `kmx tools govern`), and a migration that silently did
// both would be making that decision for the adopter.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// MigrateOptions is `kmx migrate`'s surface. There is no flag here that
// can carry credential material: the credential the application will
// present is minted by the plane, and the token the plane will present to
// Orka is minted by the API server — both go from a reply into a Secret
// without passing through a file, an argument or the environment.
type MigrateOptions struct {
	// Deployment and Namespace locate the adopter's workload. Container
	// is optional: with one container there is nothing to choose, and
	// with several the one that talks to a model has to be named.
	Deployment string
	Namespace  string
	Container  string
	// Model is the name the endpoint will be asked to resolve. Never
	// inferred — Orka resolves a model against its own Provider objects,
	// and guessing one produces a refusal an adopter cannot place.
	Model string
	// Upstream is which committed model upstream to point at. The
	// default sends the header that turns Orka's compatible endpoint
	// into a transparent proxy; `orka-coordinator` is the same endpoint
	// with Orka's default behaviour, and exists to be measured.
	Upstream string
	// Credential is the plane credential issued for this application, and
	// Secret is where its token is stored in the application's namespace.
	Credential string
	Secret     string
	// OrkaNamespace and ServiceAccount are the identity the PLANE
	// presents to Orka. TokenDuration is what is asked of the API server;
	// what it grants may be less, and what it granted is printed.
	OrkaNamespace  string
	ServiceAccount string
	TokenDuration  string
	// BaseURLVar, KeyVar and ModelVar are the application's own variable
	// names for the three things a migration rewrites.
	BaseURLVar string
	KeyVar     string
	ModelVar   string
	Out        string
	NoApply    bool
	DryRun     bool
}

// MigrateUpstreams are the committed model upstreams a migration may
// point at, in the order the flag's help prints them. Both reach the same
// endpoint; they differ by one header.
var MigrateUpstreams = []string{"orka", "orka-coordinator"}

// OrkaTokenSecret is where the plane reads the ServiceAccount token for
// Orka. The name is the one k8s/plane/proxy.yaml mounts, and the key is
// the one the committed table's credential_file names.
const OrkaTokenSecret = "kaimahi-orka-token"

func (a *App) migrateDefaults(opt MigrateOptions) MigrateOptions {
	if opt.Upstream == "" {
		opt.Upstream = MigrateUpstreams[0]
	}
	if opt.Credential == "" {
		opt.Credential = opt.Deployment
	}
	if opt.Secret == "" {
		opt.Secret = "kaimahi-" + opt.Deployment + "-token"
	}
	if opt.OrkaNamespace == "" {
		opt.OrkaNamespace = "orka-system"
	}
	if opt.ServiceAccount == "" {
		opt.ServiceAccount = "kaimahi-migrate"
	}
	if opt.TokenDuration == "" {
		opt.TokenDuration = "720h"
	}
	if opt.BaseURLVar == "" {
		opt.BaseURLVar = scaffold.MigrateBaseURLVar
	}
	if opt.KeyVar == "" {
		opt.KeyVar = scaffold.MigrateKeyVar
	}
	if opt.ModelVar == "" {
		opt.ModelVar = scaffold.MigrateModelVar
	}
	return opt
}

// Migrate runs the whole path: read the workload, check the endpoint can
// serve the model asked for, give the seam an identity there, issue the
// application a credential here, open the network, and hand back the one
// patch that changes the adopter's own object.
func (a *App) Migrate(opt MigrateOptions) error {
	started := a.timeNow()
	opt = a.migrateDefaults(opt)
	// --namespace has no default on purpose. Every other kmx command's
	// namespace default is the agent namespace, and an application this
	// project did not write does not live there; a migration that
	// defaulted would repoint whatever happened to share a name in the
	// wrong place.
	if strings.TrimSpace(opt.Namespace) == "" {
		return fmt.Errorf("kmx migrate: --namespace is required — the namespace YOUR application runs in. " +
			"There is no default: this command repoints a workload, and the wrong namespace is the wrong workload")
	}
	if strings.TrimSpace(opt.Model) == "" {
		return fmt.Errorf("kmx migrate: --model is required — the name the endpoint is to resolve " +
			"(Orka resolves <provider>/<model> against its own Provider objects). It is never inferred")
	}
	if err := admin.ValidCredentialName(opt.Credential); err != nil {
		return err
	}
	known := false
	for _, u := range MigrateUpstreams {
		if opt.Upstream == u {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("kmx migrate: --upstream %q is not one of this plane's committed model upstreams (%s). "+
			"An upstream carrying a credential and a header is a reviewed entry in k8s/plane/upstreams.yaml, "+
			"not something a command invents", opt.Upstream, strings.Join(MigrateUpstreams, ", "))
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	spec := scaffold.MigrateSpec{
		Namespace: opt.Namespace, Deployment: opt.Deployment, Container: opt.Container,
		Upstream: opt.Upstream, Model: opt.Model, Secret: opt.Secret,
		BaseURLVar: opt.BaseURLVar, KeyVar: opt.KeyVar, ModelVar: opt.ModelVar,
		OrkaNamespace: opt.OrkaNamespace, ServiceAccount: opt.ServiceAccount,
	}

	// 1. Read the workload. Everything after this is decided from what
	//    the cluster actually says, not from what the flags asked for.
	if err := a.requireNamespace(opt.Namespace, "--namespace"); err != nil {
		return err
	}
	wiring, err := a.readModelWiring(opt)
	if err != nil {
		return err
	}
	spec.Container = wiring.Container
	a.notef("%s/%s container %q reads %s today:", opt.Namespace, opt.Deployment, wiring.Container, opt.BaseURLVar)
	a.notef("  %s = %s (%s)", opt.BaseURLVar, wiring.BaseURL, wiring.BaseURLFrom)
	if wiring.ModelFrom == "" {
		a.notef("NOTE: %s is not set on this container, so the patch introduces it. If the application", opt.ModelVar)
		a.notef("  names its model in another variable, re-run with --model-var; a migrated application")
		a.notef("  asking for a model Orka has no Provider for is refused at the endpoint, not here.")
	} else {
		a.notef("  %s = %s (%s)", opt.ModelVar, wiring.Model, wiring.ModelFrom)
	}

	// 2. Can the endpoint serve what is being asked of it? Checked before
	//    anything is written, because a Provider that does not exist is
	//    the difference between a migration and an outage.
	if err := a.requireNamespace(opt.OrkaNamespace, "--orka-namespace"); err != nil {
		return err
	}
	if err := a.checkOrkaProvider(opt); err != nil {
		return err
	}

	identity, err := scaffold.GenerateMigrateIdentity(spec)
	if err != nil {
		return err
	}
	access, err := scaffold.GenerateMigrateSeamAccess(spec)
	if err != nil {
		return err
	}
	patch, err := scaffold.GenerateMigratePatch(spec)
	if err != nil {
		return err
	}

	path := opt.Out
	if path == "" {
		path = filepath.Join("migrations", opt.Deployment+".yaml")
	}
	patchPath := strings.TrimSuffix(path, filepath.Ext(path)) + "-patch.yaml"
	// Re-runnable on purpose: minting a fresh token for an expiring one is
	// the same command, and a file this command would have written
	// verbatim carries no operator work to lose. A file that DIFFERS is
	// still refused — that one may be theirs.
	unchanged, err := scaffold.WriteNewOrIdentical(path, identity+"---\n"+access)
	if err != nil {
		return err
	}
	a.notef("%s %s — the identity the seam presents to Orka, and the seam's", wrote(unchanged), path)
	a.notef("allowance for namespace %s. kmx applies both.", opt.Namespace)
	unchanged, err = scaffold.WriteNewOrIdentical(patchPath, patch)
	if err != nil {
		return err
	}
	a.notef("%s %s — the four variables and one mounted file to merge into", wrote(unchanged), patchPath)
	a.notef("YOUR Deployment, which kmx does not apply.")

	if opt.NoApply {
		a.notef("")
		a.notef("Not applied (--no-apply). Review them, then:")
		a.notef("  kubectl --context %s apply -f %s", a.Cfg.KubeContext, path)
		a.notef("  kmx migrate %s --namespace %s --model %s   # to finish the credential and the token",
			opt.Deployment, opt.Namespace, opt.Model)
		return nil
	}

	if err := a.Guard(fmt.Sprintf("migrate %s/%s onto the %q model seam", opt.Namespace, opt.Deployment, opt.Upstream),
		a.operationCommand("migrate", opt.Deployment, "--namespace", opt.Namespace, "--model", opt.Model)); err != nil {
		return err
	}
	if opt.DryRun {
		return a.kubectlRun("apply", "--dry-run=server", "-f", path)
	}
	if err := a.kubectlRun("apply", "-f", path); err != nil {
		return err
	}

	// 3. The token the plane will present. It goes from the API server's
	//    reply into a Secret through a pipe — not argv, not a file, not a
	//    log — and the expiry the API server GRANTED is printed, which is
	//    not always the one that was asked for.
	if err := a.mintOrkaToken(opt); err != nil {
		return err
	}
	// The proxy reads its upstream credentials per request, but the
	// Secret is a mounted volume: a pod that started without it has
	// nothing at that path until it restarts.
	if err := a.rollProxy(); err != nil {
		return err
	}

	// 4. The credential the application will present, and the authority
	//    it will verify the seam with.
	if err := a.session(func(client *admin.Client) error {
		return a.issueCredential(client, opt.Credential, GovernOptions{
			Secret: opt.Secret, SecretNamespace: opt.Namespace, Command: "kmx migrate",
		}, false, false)
	}); err != nil {
		return err
	}
	if err := a.publishPlaneAuthority(opt.Namespace); err != nil {
		return err
	}

	a.notef("")
	a.notef("Everything kmx owns is applied. One command changes YOUR Deployment:")
	a.notef("  kubectl --context %s -n %s patch deployment %s --patch-file %s",
		a.Cfg.KubeContext, opt.Namespace, opt.Deployment, patchPath)
	a.notef("")
	a.notef("After it rolls, the application's model calls are:")
	a.notef("  authenticated   the seam refuses an unknown credential, and Orka refuses an")
	a.notef("                  unauthenticated caller. The application holds a kmh_ token for")
	a.notef("                  the plane and no model credential at all")
	a.notef("  Provider-scoped the caller names a model, never a URL; Orka resolves it against")
	a.notef("                  its own Provider objects and refuses a name none allows")
	a.notef("  recorded        every call is a row: `kmx flow %s`", opt.Credential)
	a.notef("")
	a.notef("These values belong in the application's own release afterwards — a `helm upgrade`")
	a.notef("re-renders the Deployment and takes the patch back out:")
	a.notef("  %s=%s", opt.BaseURLVar, scaffold.MigrateBaseURL(opt.Upstream))
	a.notef("  %s=%s", opt.ModelVar, opt.Model)
	a.notef("  %s from Secret %s key api-key", opt.KeyVar, opt.Secret)
	a.notef("  SSL_CERT_FILE=%s", scaffold.MigrateCAMount+"/"+scaffold.PlaneCAKey)
	a.complete("Migrated", started)
	return nil
}

// modelWiring is what the workload says about its own model
// configuration, read from the live object rather than assumed.
type modelWiring struct {
	Container   string
	BaseURL     string
	BaseURLFrom string
	Model       string
	ModelFrom   string
}

// readModelWiring finds the container that talks to a model and reports
// what it is configured with today.
//
// It refuses rather than guesses in the two places a guess would be
// expensive: several containers with no --container, and a container with
// no base-URL variable anywhere. The second is the load-bearing one — a
// patch that sets a variable the application never reads produces a
// migration that reports success and changes nothing, which is the worst
// outcome available here.
func (a *App) readModelWiring(opt MigrateOptions) (modelWiring, error) {
	raw, err := a.kubectlCapture("-n", opt.Namespace, "get", "deployment", opt.Deployment, "-o", "json")
	if err != nil {
		return modelWiring{}, err
	}
	var deployment struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name      string          `json:"name"`
							Value     string          `json:"value"`
							ValueFrom json.RawMessage `json:"valueFrom"`
						} `json:"env"`
						EnvFrom []struct {
							// Prefix is put in FRONT of every key the source
							// carries, so a ConfigMap key and the variable the
							// container actually receives are not the same
							// string. Ignoring it would look up the wrong key
							// and report a value the container never sees.
							Prefix       string `json:"prefix"`
							ConfigMapRef *struct {
								Name string `json:"name"`
							} `json:"configMapRef"`
						} `json:"envFrom"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(raw), &deployment); err != nil {
		return modelWiring{}, fmt.Errorf("cannot read Deployment %s/%s: %w", opt.Namespace, opt.Deployment, err)
	}
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return modelWiring{}, fmt.Errorf("Deployment %s/%s has no containers", opt.Namespace, opt.Deployment)
	}
	index := -1
	switch {
	case opt.Container != "":
		for i, c := range containers {
			if c.Name == opt.Container {
				index = i
			}
		}
		if index < 0 {
			names := make([]string, 0, len(containers))
			for _, c := range containers {
				names = append(names, c.Name)
			}
			return modelWiring{}, fmt.Errorf("Deployment %s/%s has no container %q (it has: %s)",
				opt.Namespace, opt.Deployment, opt.Container, strings.Join(names, ", "))
		}
	case len(containers) == 1:
		index = 0
	default:
		names := make([]string, 0, len(containers))
		for _, c := range containers {
			names = append(names, c.Name)
		}
		return modelWiring{}, fmt.Errorf("Deployment %s/%s has %d containers (%s) — name the one that talks to a "+
			"model with --container. Repointing the wrong one changes nothing and says nothing",
			opt.Namespace, opt.Deployment, len(containers), strings.Join(names, ", "))
	}

	container := containers[index]
	wiring := modelWiring{Container: container.Name}
	// The order here is Kubernetes': every envFrom source in the order it
	// is listed, and then the container's own `env`, which wins. Reading
	// it the other way round — first source found wins — would name the
	// wrong ConfigMap in the report, and the report is the operator's
	// whole basis for believing the right container was found.
	for _, from := range container.EnvFrom {
		if from.ConfigMapRef == nil {
			// A Secret is not where a base URL or a model name belongs,
			// and kmx does not read one looking for them. If an
			// application puts them there, the refusal below is the honest
			// answer rather than a value read out of a Secret.
			continue
		}
		values, err := a.configMapValues(opt.Namespace, from.ConfigMapRef.Name)
		if err != nil {
			return modelWiring{}, err
		}
		// The variable the container receives is prefix + key, so the key
		// to look up is the variable with that prefix taken off — and a
		// variable that does not start with the prefix cannot come from
		// this source at all.
		if v, ok := prefixed(values, from.Prefix, opt.BaseURLVar); ok {
			wiring.BaseURL, wiring.BaseURLFrom = v, "from ConfigMap "+from.ConfigMapRef.Name
		}
		if v, ok := prefixed(values, from.Prefix, opt.ModelVar); ok {
			wiring.Model, wiring.ModelFrom = v, "from ConfigMap "+from.ConfigMapRef.Name
		}
	}
	for _, env := range container.Env {
		// A variable set from a reference has its name here and its value
		// somewhere kmx did not look. Saying so beats printing "(empty)",
		// which reads as a misconfiguration when the variable is in fact
		// set. Either way the patch overrides it: an explicit `env` entry
		// wins over both of these.
		value := env.Value
		if env.Value == "" && len(env.ValueFrom) > 0 {
			value = "(from a reference kmx did not read)"
		}
		switch env.Name {
		case opt.BaseURLVar:
			wiring.BaseURL, wiring.BaseURLFrom = value, "set on the container"
		case opt.ModelVar:
			wiring.Model, wiring.ModelFrom = value, "set on the container"
		}
	}
	if wiring.BaseURLFrom == "" {
		return modelWiring{}, fmt.Errorf("container %q of %s/%s does not read %s — neither directly nor from a "+
			"ConfigMap it takes envFrom. This command repoints an application that reads a base URL from its "+
			"environment; if this one names it differently, pass --base-url-var, and if it does not read one at "+
			"all, its model endpoint is not configurable and nothing kmx writes would change it",
			container.Name, opt.Namespace, opt.Deployment, opt.BaseURLVar)
	}
	if wiring.BaseURL == "" {
		wiring.BaseURL = "(empty)"
	}
	return wiring, nil
}

// prefixed looks up the ConfigMap key behind an environment variable
// that arrives through an envFrom source carrying a prefix. With no
// prefix — which is every chart this has met — it is a plain lookup.
func prefixed(values map[string]string, prefix, variable string) (string, bool) {
	if prefix != "" && !strings.HasPrefix(variable, prefix) {
		return "", false
	}
	v, ok := values[strings.TrimPrefix(variable, prefix)]
	return v, ok
}

func (a *App) configMapValues(namespace, name string) (map[string]string, error) {
	raw, err := a.kubectlCapture("-n", namespace, "get", "configmap", name, "-o", "jsonpath={.data}")
	if err != nil {
		if isNotFound(err) {
			// A workload may reference a ConfigMap that does not exist
			// yet; that is the application's business, not a reason to
			// refuse to read the rest of its environment.
			return map[string]string{}, nil
		}
		return nil, err
	}
	values := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("cannot read ConfigMap %s/%s: %w", namespace, name, err)
	}
	return values, nil
}

// checkOrkaProvider asks the cluster whether the model can be resolved
// before anything is written.
//
// Orka resolves `<provider>/<model>` against its own Provider objects and
// refuses a name none allows. That refusal is the Provider scoping this
// migration is buying, so it is worth reaching for it early, where it is
// one message instead of an application that rolls and then fails every
// turn. A Provider this cannot see is a warning rather than a refusal:
// the objects may be in another namespace, and kmx does not own the rule.
func (a *App) checkOrkaProvider(opt MigrateOptions) error {
	provider, _, found := strings.Cut(opt.Model, "/")
	if !found || provider == "" {
		a.notef("NOTE: %q does not name a Provider. Orka resolves a model as <provider>/<model> and falls back to "+
			"a Provider named 'default'; if there is none, every call is refused.", opt.Model)
		return nil
	}
	raw, err := a.kubectlCapture("-n", opt.OrkaNamespace, "get", "provider", provider,
		"-o", "jsonpath={.status.ready}")
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("Orka has no Provider %q in namespace %s, so a call naming model %q would be "+
				"refused there. Nothing was written. Create the Provider (or pass --model with a name one of "+
				"its Providers allows) and run this again",
				provider, opt.OrkaNamespace, opt.Model)
		}
		a.notef("NOTE: could not read Provider %q in %s (%v) — continuing, because the objects may live "+
			"elsewhere. If model %q is not one Orka can resolve, its own refusal will say so.",
			provider, opt.OrkaNamespace, err, opt.Model)
		return nil
	}
	if strings.TrimSpace(raw) != "true" {
		return fmt.Errorf("Orka's Provider %q in namespace %s is not ready, so a call naming model %q would be "+
			"refused there. Nothing was written", provider, opt.OrkaNamespace, opt.Model)
	}
	a.notef("Orka Provider %q is ready, so model %q resolves there.", provider, opt.Model)
	return nil
}

// mintOrkaToken asks the API server for a token for the seam's
// ServiceAccount and stores it where the proxy mounts it.
//
// What is asked for and what is granted are different numbers — the API
// server caps a TokenRequest at its own maximum — so the granted expiry is
// read out of the reply and printed. A credential whose deadline nobody
// learns is discovered as an outage.
func (a *App) mintOrkaToken(opt MigrateOptions) error {
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s create token %s --duration=%s # (into Secret %s, from the pipe)\n",
		a.Cfg.KubeContext, opt.OrkaNamespace, opt.ServiceAccount, opt.TokenDuration, OrkaTokenSecret)
	raw, err := quiet.Capture("kubectl", a.kubectl("-n", opt.OrkaNamespace, "create", "token",
		opt.ServiceAccount, "--duration="+opt.TokenDuration, "-o", "json")...)
	if err != nil {
		return fmt.Errorf("minting a token for ServiceAccount %s/%s: %w", opt.OrkaNamespace, opt.ServiceAccount, err)
	}
	var request struct {
		Status struct {
			Token      string `json:"token"`
			Expiration string `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &request); err != nil || request.Status.Token == "" {
		return fmt.Errorf("the API server's token reply for %s/%s could not be read; nothing was stored",
			opt.OrkaNamespace, opt.ServiceAccount)
	}
	manifest := secretManifest(OrkaTokenSecret, scaffold.PlaneNamespace,
		map[string]string{"token": request.Status.Token},
		map[string]string{
			"app.kubernetes.io/managed-by": "kmx",
			"kaimahi.dev/service-account":  opt.OrkaNamespace + "/" + opt.ServiceAccount,
			"kaimahi.dev/token-expires":    request.Status.Expiration,
		})
	if err := a.applySecretIn(scaffold.PlaneNamespace, manifest, OrkaTokenSecret); err != nil {
		return err
	}
	a.notef("The seam's token for %s/%s expires %s.", opt.OrkaNamespace, opt.ServiceAccount, request.Status.Expiration)
	a.notef("  Re-run `kmx migrate` to mint another; the application's own credential is untouched by that.")
	return nil
}

// wrote names what happened to a scaffolded file, so a re-run says
// "Unchanged" rather than claiming to have written what was already there.
func wrote(unchanged bool) string {
	if unchanged {
		return "Unchanged"
	}
	return "Wrote"
}
