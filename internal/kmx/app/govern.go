package app

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// GovernOptions are the knobs of the transitional lift governance phase.
type GovernOptions struct {
	// Agent is the agent switched onto the governed preset.
	Agent string
	// Preset is the governed ModelConfig to switch it to.
	Preset string
	// Secret is the agent-side Secret the issued token is stored in.
	Secret string
	// SecretNamespace is where that Secret lives.
	SecretNamespace string
	// TTLSeconds, when set, is the issued credential's lifetime.
	TTLSeconds *int64
}

// Govern issues a governed credential and switches a legacy Agent onto the
// governed preset.
//
// TRANSITIONAL, and reachable from exactly one place: the lift `agents`
// phase, which still applies the two legacy demonstration Agents onto a
// managed cluster. There is no `kmx govern` any more — the public command
// was retired with the rest of the legacy runtime — and this function goes
// with the lift payload it serves, in the slice that retires it. Nothing new
// may call it.
//
// What it means, unchanged while it lives: the agent is handed a
// Kaimahi-ISSUED opaque token, never an upstream key — the plane stores only
// that token's hash, and the real keys stay with the proxy. From here every
// call the agent makes is authenticated, budget-checked and ledgered.
func (a *App) Govern(credential string, opt GovernOptions) error {
	if err := validCredentialName(credential); err != nil {
		return err
	}
	if opt.Agent == "" || opt.Preset == "" || opt.Secret == "" || opt.SecretNamespace == "" {
		return fmt.Errorf("lift governance: agent, preset and secret must all be named")
	}
	// Only the two embedded presets remain reachable: they name Secret
	// %s/%s, so any other destination would issue a token the agent cannot
	// read. The operator-owned custom-preset path went with `kmx govern`.
	if opt.Preset != "governed-ollama" && opt.Preset != "governed-copilot" {
		return fmt.Errorf("lift governance applies the embedded governed presets only, not %q", opt.Preset)
	}
	if opt.Secret != config.GovernedSecret || opt.SecretNamespace != config.DefaultNamespace {
		return fmt.Errorf("lift governance: the applied presets reference Secret %s/%s; another destination would issue an unusable token", config.DefaultNamespace, config.GovernedSecret)
	}
	if err := admin.CheckCredentialTTL(opt.TTLSeconds); err != nil {
		return err
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}

	client, err := admin.Open(a, a.Cfg.AdminPort, a.Err)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := a.issueCredential(client, credential, CredentialOptions{
		Agent:           opt.Agent,
		Secret:          opt.Secret,
		SecretNamespace: opt.SecretNamespace,
		TTLSeconds:      opt.TTLSeconds,
		Command:         "kmx lift --step agents",
	}, false); err != nil {
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
	// agent is switched to depends on the environment. They are applied by
	// the switch below, which has to watch the preset's generation across
	// the apply.
	presets := []string{"models/governed-ollama.yaml", "models/governed-copilot.yaml"}

	// Only a genuine NotFound may skip the switch. Collapsing every failure
	// into "absent" would print the reassuring NOTE, exit 0, and leave the
	// agent on an UNGOVERNED preset — spending outside the plane. An
	// unreachable API server, an expired credential, an RBAC denial and a
	// wrong context all look exactly like that if you do not look.
	_, err = a.kubectlCapture("-n", config_kagentNamespace, "get", "agents.kagent.dev", opt.Agent, "-o", "name")
	switch {
	case err == nil:
		return a.UsePreset(opt.Agent, opt.Preset, presets)
	case isNotFound(err):
		for _, name := range presets {
			if err := a.apply(name); err != nil {
				return err
			}
		}
		a.notef("NOTE: agent %s does not exist, so nothing was switched. %s is on the cluster,\n"+
			"  but an agent created later starts on the keyless preset %s and would spend outside the plane.",
			opt.Agent, opt.Preset, config.KeylessModelConfig)
		return nil
	default:
		return fmt.Errorf("cannot tell whether agent %s exists (refusing to leave it ungoverned): %w", opt.Agent, err)
	}
}
