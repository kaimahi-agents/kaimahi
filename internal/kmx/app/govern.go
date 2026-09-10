package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// errCredentialNotBound marks the case where the plane already holds a
// credential of this name and the Secret that should carry its token is
// absent. The token is shown once and cannot be recovered, so the
// situation is unrecoverable either way — but WHY it happened decides
// what to do about it, and only the caller knows whether the name could
// belong to something else.
var errCredentialNotBound = errors.New("credential exists and its Secret is not bound")

// GovernOptions are `kmx govern`'s knobs, defaulted to what `make govern`
// uses on kind so the delegating recipe passes nothing surprising.
type GovernOptions struct {
	// Agent is the agent switched onto the governed preset.
	Agent string
	// Preset is the governed ModelConfig to switch it to.
	Preset string
	// Secret is the agent-side Secret the issued token is stored in.
	Secret string
	// SecretNamespace is where that Secret lives.
	SecretNamespace string
	// TTLSeconds, when set, is the issued credential's lifetime. Nil
	// takes the plane's default; there is no way to ask for "never",
	// because a credential with no expiry is the closed legacy class.
	TTLSeconds *int64
	// Command names the command an operator would re-run with a different
	// --secret, so the refusal below names `kmx govern` or
	// `kmx tools govern` — whichever they actually typed.
	Command string
}

// Govern issues the governed credential, applies the governed presets, and
// puts the agent behind the plane.
//
// This is `make govern`. What it means, unchanged: the agent is handed a
// Kaimahi-ISSUED opaque token, never an upstream key — the plane stores only
// that token's hash, and the real keys stay with the proxy. From here every
// call the agent makes is authenticated, budget-checked and ledgered.
func (a *App) Govern(credential string, opt GovernOptions) error {
	if err := validCredentialName(credential); err != nil {
		return err
	}
	if opt.Agent == "" || opt.Preset == "" || opt.Secret == "" || opt.SecretNamespace == "" {
		return fmt.Errorf("kmx govern: agent, preset and secret must all be named")
	}
	embedded := opt.Preset == "governed-ollama" || opt.Preset == "governed-copilot"
	if embedded && (opt.Secret != config.GovernedSecret || opt.SecretNamespace != config.DefaultNamespace) {
		return fmt.Errorf("kmx govern: the applied presets reference Secret %s/%s; unsupported --secret or --secret-namespace would issue an unusable token", config.DefaultNamespace, config.GovernedSecret)
	}
	if err := admin.CheckCredentialTTL(opt.TTLSeconds); err != nil {
		return err
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if !embedded {
		// Custom presets are operator-owned. Verify what the agent will use
		// before minting a token, rather than rewriting the preset to fit it.
		raw, err := a.kubectlCapture("-n", config_kagentNamespace, "get", "modelconfig", opt.Preset, "-o", "json")
		if err != nil {
			return fmt.Errorf("cannot inspect ModelConfig %q before governance; nothing issued: %w", opt.Preset, err)
		}
		var model modelStatus
		if err := json.Unmarshal([]byte(raw), &model); err != nil {
			return fmt.Errorf("cannot inspect ModelConfig %q before governance; nothing issued: %w", opt.Preset, err)
		}
		base, err := url.Parse(model.Spec.OpenAI.BaseURL)
		if err != nil || model.Spec.Provider != "OpenAI" || classifySeam(model.Spec.OpenAI.BaseURL, planeProxyService) != seamGoverned ||
			base.Scheme != "http" || base.Port() != "8080" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" ||
			!strings.HasPrefix(base.Path, "/upstream/") || strings.TrimPrefix(base.Path, "/upstream/") == "" || base.RawPath != "" || path.Clean(base.Path) != strings.TrimSuffix(base.Path, "/") {
			return fmt.Errorf("ModelConfig %q is not a supported OpenAI route through the Kaimahi proxy; nothing issued", opt.Preset)
		}
		// modelStatus carries the route and Secret name; these additional
		// reference fields matter for issuance, but not the status view.
		var reference struct {
			Metadata struct{ Namespace string } `json:"metadata"`
			Spec     struct {
				APIKeySecretKey string `json:"apiKeySecretKey"`
			} `json:"spec"`
		}
		if err := json.Unmarshal([]byte(raw), &reference); err != nil {
			return fmt.Errorf("cannot inspect ModelConfig %q Secret reference; nothing issued: %w", opt.Preset, err)
		}
		if reference.Metadata.Namespace != config_kagentNamespace || opt.SecretNamespace != reference.Metadata.Namespace ||
			model.Spec.APIKeySecret != opt.Secret || reference.Spec.APIKeySecretKey != "api-key" {
			return fmt.Errorf("ModelConfig %q must reference Secret %s/%s key api-key in the agent's namespace %s; nothing issued", opt.Preset, opt.SecretNamespace, opt.Secret, config_kagentNamespace)
		}
	}
	args := []string{"govern", credential, "--agent", opt.Agent, "--preset", opt.Preset, "--secret", opt.Secret, "--secret-namespace", opt.SecretNamespace}
	if opt.TTLSeconds != nil {
		args = append(args, "--ttl", fmt.Sprint(*opt.TTLSeconds))
	}
	if err := a.Guard(fmt.Sprintf("govern agent %q through the Kaimahi plane (credential %q)", opt.Agent, credential),
		a.operationCommand(args...)); err != nil {
		return err
	}

	client, err := admin.Open(a, a.Cfg.AdminPort, a.Err)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := a.issueCredential(client, credential, opt, false, false); err != nil {
		return err
	}

	// The presets below name a CA Secret in the agent namespace, and kagent
	// refuses a ModelConfig whose named Secret is absent — the seam would
	// report Accepted=false rather than switching. Published here, not only
	// by `kmx plane`, because the plane can be deployed before kagent
	// exists and this is the first moment the namespace is certain to.
	if err := a.publishPlaneAuthority(config_kagentNamespace); err != nil {
		return err
	}

	// Both governed presets are applied on every target — which one the
	// agent is switched to depends on the environment. On kind that is
	// governed-ollama, the keyless one, which is why governing on kind needs
	// no MODEL-PROVIDER credential at all. A Secret is still required and
	// created — the one `--secret` names — but kmx writes the plane's own
	// kmh_ token into it rather than capturing a provider key.
	// They are applied by the switch
	// below, which has to watch the preset's generation across the apply.
	presets := []string{"models/governed-ollama.yaml", "models/governed-copilot.yaml"}

	// Only a genuine NotFound may skip the switch. Collapsing every failure
	// into "absent" would print the reassuring NOTE, exit 0, and leave the
	// agent on an UNGOVERNED preset — spending outside the plane. An
	// unreachable API server, an expired credential, an RBAC denial and a
	// wrong context all look exactly like that if you do not look.
	_, err = a.kubectlCapture("-n", config_kagentNamespace, "get", "agent", opt.Agent, "-o", "name")
	switch {
	case err == nil:
		return a.UsePreset(opt.Agent, opt.Preset, presets)
	case isNotFound(err):
		for _, name := range presets {
			if err := a.apply(name); err != nil {
				return err
			}
		}
		// Say what actually happens, not what would be reassuring. `kmx up`
		// creates hello-world on the KEYLESS preset (k8s/hello-world.yaml
		// pins it), so an agent created after this runs UNGOVERNED until
		// govern is run again. The Makefile's managed branch says the same
		// thing for the same reason.
		a.notef("NOTE: agent %s does not exist yet, so nothing was switched. %s is on the cluster,\n"+
			"  but an agent created later starts on the keyless preset %s — re-run\n"+
			"  `kmx govern %s` once %s exists, or it will spend outside the plane.",
			opt.Agent, opt.Preset, config.KeylessModelConfig, credential, opt.Agent)
		return nil
	default:
		return fmt.Errorf("cannot tell whether agent %s exists (refusing to leave it ungoverned): %w", opt.Agent, err)
	}
}

// GovernInteractiveModel gives the active agent its own credential, Secret,
// and ModelConfig so in-chat governance cannot merge one agent's spend and
// budget identity with another's.
func (a *App) GovernInteractiveModel(agent string) error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	if err := validCredentialName(agent); err != nil {
		return err
	}
	secret := governedResourceName("kmx-token", agent)
	preset := governedResourceName("kmx-governed-ollama", agent)
	credential := governedResourceName("kmx-model", agent)
	opt := GovernOptions{Agent: agent, Preset: preset, Secret: secret, SecretNamespace: config.DefaultNamespace}
	if err := a.Guard(fmt.Sprintf("govern agent %q's model seam through the Kaimahi plane (credential %q)", agent, credential), a.operationCommand("agent", "chat", "--interactive", agent)); err != nil {
		return err
	}
	model, err := a.activeModelName(agent)
	if err != nil {
		return err
	}
	secretExists, err := a.validateInteractiveResourceOwnership(agent, secret, preset)
	if err != nil {
		return err
	}
	client, err := admin.Open(a, a.Cfg.AdminPort, a.Err)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := a.issueCredential(client, credential, opt, true, secretExists); err != nil {
		return err
	}
	if err := a.publishPlaneAuthority(config_kagentNamespace); err != nil {
		return err
	}
	manifest, err := interactiveModelManifest(preset, secret, model, true, agent)
	if err != nil {
		return err
	}
	return a.usePreset(agent, preset, func() error {
		fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (agent-specific governed ModelConfig %s)\n", a.Cfg.KubeContext, preset)
		quiet := *a.Run
		quiet.Echo = false
		return quiet.RunStdin(manifest, "kubectl", a.kubectl("apply", "-f", "-")...)
	})
}

func interactiveModelManifest(preset, secret, model string, governed bool, agent string) ([]byte, error) {
	spec := map[string]any{"model": model}
	if governed {
		spec["provider"] = "OpenAI"
		spec["apiKeySecret"] = secret
		spec["apiKeySecretKey"] = "api-key"
		// DERIVED, not spelled out. This was a second copy of the seam URL
		// and it is exactly the copy that stops agreeing with the first: the
		// committed presets moved to TLS and this literal would have stayed
		// plaintext, which nothing refuses — a ModelConfig with a TLS block
		// beside an http:// baseUrl is admitted and the TLS block is inert.
		spec["openAI"] = map[string]string{"baseUrl": scaffold.ProxyBaseURL}
		// What makes that URL verifiable: the authority's certificate, in
		// this namespace, named here so the controller mounts it into the
		// agent. Without it kagent uses the system trust store, which has
		// never heard of the plane.
		spec["tls"] = map[string]string{
			"caCertSecretRef": scaffold.PlaneCASecret,
			"caCertSecretKey": scaffold.PlaneCAKey,
		}
	} else {
		spec["provider"] = "Ollama"
		spec["ollama"] = map[string]string{"host": "http://ollama.ollama.svc.cluster.local:11434"}
	}
	return json.Marshal(map[string]any{
		"apiVersion": "kagent.dev/v1alpha2",
		"kind":       "ModelConfig",
		"metadata": map[string]any{
			"name": preset, "namespace": config.DefaultNamespace,
			"annotations": map[string]string{"app.kubernetes.io/managed-by": "kmx", "kaimahi.dev/chat-agent": agent},
		},
		"spec": spec,
	})
}

// issueCredential mints the credential and stores its token as the
// agent-side Secret, reconciling the already-issued case as
// scripts/plane-admin.sh does (minus its `GOVERNED_SECRET=-` form, which
// discards the token for the inbound bridge's signed hooks — an inbound
// feature kmx does
// not have).
//
// The token is shown EXACTLY ONCE, at issue time, and cannot be recovered.
// That is what makes both the check before the POST and the 409 branch below
// more than politeness.
func (a *App) issueCredential(client *admin.Client, credential string, opt GovernOptions, interactive, secretExists bool) error {
	// Whose token is in that Secret? Asked BEFORE issuing, because the
	// answer can forbid the whole operation: `kmx govern demo` while the
	// Secret holds hello-world's token would otherwise mint demo's
	// credential, overwrite the Secret, and destroy the only copy of
	// hello-world's token — leaving a live credential nothing can use. The
	// 409 branch refuses exactly this once the credential already exists;
	// the first issue of a SECOND name has to refuse it too, and refusing
	// before the POST also avoids leaving an orphan credential row behind.
	bound, err := a.boundCredential(opt)
	if err != nil {
		return err
	}
	if bound != "" && bound != credential {
		return a.wrongCredentialError(bound, credential, opt)
	}

	issue := map[string]any{"name": credential}
	if opt.TTLSeconds != nil {
		issue["ttl_seconds"] = *opt.TTLSeconds
	}
	status, body, err := client.Do(http.MethodPost, "/admin/credentials", issue)
	if err != nil {
		return err
	}

	if status == http.StatusConflict {
		return a.reconcileExistingCredential(credential, opt, interactive)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("issuing credential %q failed (HTTP %d): %s",
			credential, status, strings.TrimSpace(string(body)))
	}

	token, err := admin.TokenFrom(body)
	if err != nil {
		return err
	}
	// Straight from the reply into the manifest into kubectl's stdin. The
	// token is in this process's memory and in the cluster, and nowhere
	// else: not argv, not the environment, not a file, not a log.
	manifest := secretManifest(opt.Secret, opt.SecretNamespace,
		map[string]string{"api-key": token},
		// Bind the Secret to its credential, so a later issue of a
		// DIFFERENT name detects the mismatch instead of silently reusing
		// this token.
		map[string]string{"kaimahi.dev/credential": credential, "app.kubernetes.io/managed-by": "kmx", "kaimahi.dev/chat-agent": opt.Agent})
	quiet := *a.Run
	quiet.Echo = false
	verb := credentialSecretVerb(interactive, secretExists)
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s %s -f - # (Secret %s, from the pipe)\n",
		a.Cfg.KubeContext, opt.SecretNamespace, verb, opt.Secret)
	if err := quiet.RunStdin(manifest, "kubectl",
		a.kubectl("-n", opt.SecretNamespace, verb, "-f", "-")...); err != nil {
		return err
	}
	a.notef("Governed credential %q issued; Secret %s/%s created.", credential, opt.SecretNamespace, opt.Secret)
	a.notef("The plane stores only its hash — the real upstream keys stay with the proxy.")
	if expires := admin.ExpiresFrom(body); expires != "" {
		// Said at issue time, not only when it bites: an operator who
		// never learns the deadline discovers it as an outage.
		a.notef("It expires %s — `kmx credentials` shows every deadline, `kmx credential renew %s` extends this one.",
			expires, credential)
	}
	return nil
}

func credentialSecretVerb(interactive, secretExists bool) string {
	if interactive && !secretExists {
		return "create"
	}
	return "apply"
}

// boundCredential returns the credential the agent-side Secret holds the
// token for, or "" when there is no such Secret.
//
// Only a genuine NotFound is "no Secret". Any other read failure aborts: an
// unreadable Secret answered as absent is how the overwrite this check
// exists to prevent would happen anyway.
func (a *App) boundCredential(opt GovernOptions) (string, error) {
	bound, err := a.kubectlCapture("-n", opt.SecretNamespace, "get", "secret", opt.Secret,
		"-o", `jsonpath={.metadata.annotations.kaimahi\.dev/credential}`)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("cannot read Secret %s to tell whose token it holds (refusing to overwrite it blind): %w",
			opt.Secret, err)
	}
	return strings.TrimSpace(bound), nil
}

// command is the invocation the refusals point back at.
func command(opt GovernOptions, credential string) string {
	if opt.Command != "" {
		return opt.Command
	}
	return "kmx govern " + shellArg(credential)
}

func (a *App) wrongCredentialError(bound, credential string, opt GovernOptions) error {
	return fmt.Errorf("Secret %s holds the token for credential %q, not %q — refusing.\n"+
		"  That token is the only copy; overwriting it would leave %q live in the plane and unusable.\n"+
		"  A different --secret also requires matching model/tool references; %s cannot rewire the committed presets.\n"+
		"  Keep the existing credential, use a custom preset/server with a matching Secret, or use agent-specific model governance in interactive chat.",
		opt.Secret, bound, credential, bound, command(opt, credential))
}

// reconcileExistingCredential decides what an HTTP 409 means, given what the
// agent-side Secret is bound to.
func (a *App) reconcileExistingCredential(credential string, opt GovernOptions, interactive bool) error {
	bound, err := a.boundCredential(opt)
	if err != nil {
		return err
	}
	switch bound {
	case credential:
		a.notef("Credential %q already issued and %s is bound to it; keeping both.", credential, opt.Secret)
		return nil
	case "":
		if interactive {
			return fmt.Errorf("credential %q already exists, but the agent-specific Secret %s is not safely bound to it; refusing to delete or replace either resource", credential, opt.Secret)
		}
		// Wrapped so a caller that knows a SECOND reason this can happen
		// can say so. The recovery below is right when the Secret was
		// lost; it is dangerous when the name simply belongs to somebody
		// else's workload, because the row it deletes is theirs.
		return fmt.Errorf("%w: credential %q exists in the plane but Secret %s is missing (or unlabeled).\n"+
			"  The token is shown exactly once at issue time and cannot be recovered;\n"+
			"  delete the row and re-run:\n"+
			"    kubectl --context %s -n %s exec deploy/kaimahi-postgres -- \\\n"+
			"      psql -U kaimahi -c \"DELETE FROM credential WHERE name='%s'\"",
			errCredentialNotBound, credential, opt.Secret, a.Cfg.KubeContext, admin.Namespace, credential)
	default:
		return a.wrongCredentialError(bound, credential, opt)
	}
}

func (a *App) activeModelName(agent string) (string, error) {
	modelConfig, err := a.liveModelConfig(agent)
	if err != nil {
		return "", err
	}
	raw, err := a.kubectlCapture("-n", config.DefaultNamespace, "get", "modelconfig", modelConfig, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("cannot read active ModelConfig %q: %w", modelConfig, err)
	}
	var resource struct {
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal([]byte(raw), &resource); err != nil {
		return "", fmt.Errorf("active ModelConfig %q returned invalid JSON", modelConfig)
	}
	model, _ := resource.Spec["model"].(string)
	provider, _ := resource.Spec["provider"].(string)
	compatible := strings.EqualFold(provider, "Ollama") || usesKaimahiModelProxy(resource.Spec)
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("active ModelConfig %q does not expose a model name", modelConfig)
	}
	if !compatible {
		return "", fmt.Errorf("active ModelConfig %q uses provider %q, not Ollama or the Kaimahi Ollama proxy; refusing to change its route", modelConfig, provider)
	}
	return model, nil
}

func (a *App) validateInteractiveResourceOwnership(agent, secret, preset string) (bool, error) {
	if err := a.validateInteractiveModelOwnership(agent, preset); err != nil {
		return false, err
	}
	secretExists := false
	for _, resource := range []struct{ kind, name string }{{"secret", secret}} {
		raw, err := a.kubectlCapture("-n", config.DefaultNamespace, "get", resource.kind, resource.name, "-o", "json")
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return false, fmt.Errorf("cannot inspect %s %s before governance: %w", resource.kind, resource.name, err)
		}
		secretExists = true
		var current struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		}
		if json.Unmarshal([]byte(raw), &current) != nil || current.Metadata.Annotations["app.kubernetes.io/managed-by"] != "kmx" || current.Metadata.Annotations["kaimahi.dev/chat-agent"] != agent {
			return false, fmt.Errorf("%s %s already exists without matching kmx ownership for agent %s; refusing to overwrite it", resource.kind, resource.name, agent)
		}
		if resource.kind == "secret" {
			token, err := base64.StdEncoding.DecodeString(current.Data["api-key"])
			if err != nil || !strings.HasPrefix(string(token), "kmh_") || len(token) != 68 {
				return false, fmt.Errorf("Secret %s is owned by kmx but does not contain a valid Kaimahi token; refusing to use it", secret)
			}
		}
	}
	return secretExists, nil
}

func (a *App) validateInteractiveModelOwnership(agent, preset string) error {
	raw, err := a.kubectlCapture("-n", config.DefaultNamespace, "get", "modelconfig", preset, "-o", "json")
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("cannot inspect modelconfig %s before changing governance: %w", preset, err)
	}
	var current struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(raw), &current) != nil || current.Metadata.Annotations["app.kubernetes.io/managed-by"] != "kmx" || current.Metadata.Annotations["kaimahi.dev/chat-agent"] != agent {
		return fmt.Errorf("modelconfig %s already exists without matching kmx ownership for agent %s; refusing to overwrite it", preset, agent)
	}
	return nil
}

// validCredentialName is the script's check_name, kept because these names
// are interpolated into JSON and query strings. The plane validates again.
func validCredentialName(name string) error {
	if name == "" {
		return fmt.Errorf("usage: kmx govern <credential>")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("invalid credential name %q (want [a-z0-9-]+)", name)
		}
	}
	return nil
}
