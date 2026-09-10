package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// `kmx use` and the tool-governance verbs.
//
// The Makefile's `use`, `use-ollama`, `govern-tools`, `ungovern-tools`,
// `tool-allow` and `tool-allowlist` are the specification. All four of the
// cluster-touching ones end in the same three-deep wait — `wait_switched`,
// already carried across in use.go — because all four change what a pod
// runs, and "the object was patched" is not that.

// UseOptions are `kmx use`'s knobs.
type UseOptions struct {
	// Agent is the agent switched onto the preset. `make use` hard-codes
	// hello-world; the flag exists because `kmx use` has no PRESET= line to
	// hide a second agent behind.
	Agent string
}

// Use switches an agent onto a model preset from k8s/models/.
//
// Hosted presets need their key Secret first (checkout-only generic model
// setup, or `kmx models credential copilot`). Those are not on the ordinary
// path, so this applies the preset and switches the agent, and the Secret
// the preset NAMES is somebody else's job. A preset whose Secret is missing produces an
// agent that starts and then fails its calls; that is the behaviour
// `make use` has always had.
func (a *App) Use(preset string, opt UseOptions) error {
	if opt.Agent == "" {
		opt.Agent = config.DefaultAgent
	}
	name, err := presetManifest(preset)
	if err != nil {
		return err
	}
	if err := a.Guard(fmt.Sprintf("switch agent %q onto model preset %q", opt.Agent, preset),
		a.operationCommand("use", preset, "--agent", opt.Agent)); err != nil {
		return err
	}
	return a.UsePreset(opt.Agent, preset, []string{name})
}

// presetManifest resolves a preset NAME to the embedded manifest, refusing
// anything that is not one of the presets kmx carries.
//
// The name is checked rather than interpolated: it becomes a path into the
// embedded filesystem, and it is also the object name the agent is patched
// onto. An unknown preset lists what there is, because the failure mode this
// replaces — `kubectl apply -f k8s/models/typo.yaml` — told an operator only
// that a file was missing.
func presetManifest(preset string) (string, error) {
	if preset == "" {
		return "", fmt.Errorf("usage: kmx use <preset> — one of: %s", strings.Join(presetNames(), ", "))
	}
	for _, name := range presetNames() {
		if name == preset {
			return "models/" + preset + ".yaml", nil
		}
	}
	return "", fmt.Errorf("unknown model preset %q — kmx carries: %s", preset, strings.Join(presetNames(), ", "))
}

// presetNames lists the presets embedded in the binary, sorted.
func presetNames() []string {
	entries, err := fs.ReadDir(kaimahi.Manifests, "k8s/models")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if name := strings.TrimSuffix(e.Name(), ".yaml"); name != e.Name() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// PresetNames returns the embedded model preset names for CLI completion.
func PresetNames() []string { return presetNames() }

// ToolsOptions are the knobs `kmx tools govern` and `kmx tools ungovern`
// share, defaulted to what `make govern-tools` uses.
type ToolsOptions struct {
	// Credential is the kmh_ credential the gateway admits (CRED_TOOLS).
	Credential string
	// Agent is the agent pointed at the governed RemoteMCPServer.
	Agent string
	// Secret is the agent-side Secret that credential's token is stored in.
	Secret string
	// SecretNamespace is where that Secret lives.
	SecretNamespace string
	// Tools is the comma-separated allowlist, and the agent's selection.
	// "-" is the empty allowlist: nothing callable without a live grant.
	Tools string
	// Server is the RemoteMCPServer to govern against. Empty means
	// this repo's committed `kaimahi-tools`, which kmx carries and
	// applies. Any other name is one an operator scaffolded with
	// `kmx tools add`, which already applied it — kmx has no committed
	// copy to re-apply and must not invent one.
	Server string
}

// GovernTools puts the tools agent behind the enforcing MCP gateway
// (`make govern-tools`).
//
// The order is the recipe's, and it is not arbitrary: the credential exists
// before the allowlist that names it, the allowlist exists before the
// RemoteMCPServer whose discovery is its projection, and the agent is
// repointed last. kagent discovers tools THROUGH the gateway with this same
// credential, so `status.discoveredTools` IS the allowlist projection — an
// agent never sees a tool its credential cannot call.
func (a *App) GovernTools(opt ToolsOptions) error {
	opt = a.toolsDefaults(opt)
	if err := admin.ValidCredentialName(opt.Credential); err != nil {
		return err
	}
	tools, err := admin.ParseToolList(opt.Tools)
	if err != nil {
		return err
	}
	if opt.Server == config.DefaultToolServer && (opt.SecretNamespace != config.DefaultNamespace || opt.Secret != config.DefaultToolsSecret) {
		return fmt.Errorf("kmx tools govern: the applied RemoteMCPServer references Secret %s/%s; unsupported --secret or --secret-namespace would issue an unusable token", config.DefaultNamespace, config.DefaultToolsSecret)
	}
	if err := a.Guard(fmt.Sprintf("put agent %q behind the Kaimahi MCP gateway (credential %q)",
		opt.Agent, opt.Credential), a.operationCommand("tools", "govern", "--agent", opt.Agent, "--credential", opt.Credential,
		"--server", opt.Server, "--secret", opt.Secret, "--secret-namespace", opt.SecretNamespace, "--tools", opt.Tools)); err != nil {
		return err
	}
	// A plane can govern an application this project did not write, on a
	// cluster with no kagent at all. Six of the steps below are kagent's —
	// the seam, its verdict, the agent patch and the rollout waits — and
	// none of them is what makes a call governed. The credential and the
	// allowlist are.
	seamInstalled, err := a.kagentSeamInstalled()
	if err != nil {
		return err
	}
	// Before anything is minted: the token is shown once, so a Secret
	// namespace that does not exist must refuse here rather than after
	// the credential is live and unrecoverable.
	if err := a.requireNamespace(opt.SecretNamespace, "--secret-namespace"); err != nil {
		return err
	}
	if seamInstalled && opt.Server != config.DefaultToolServer {
		if err := a.preflightToolServer(opt.Server); err != nil {
			return err
		}
		raw, err := a.kubectlCapture("-n", opt.SecretNamespace, "get", "remotemcpserver", opt.Server, "-o", "json")
		if err != nil {
			return err
		}
		var server struct {
			Spec struct {
				HeadersFrom []struct {
					Name      string `json:"name"`
					ValueFrom struct {
						Type string `json:"type"`
						Name string `json:"name"`
						Key  string `json:"key"`
					} `json:"valueFrom"`
				} `json:"headersFrom"`
			} `json:"spec"`
		}
		if err := json.Unmarshal([]byte(raw), &server); err != nil {
			return fmt.Errorf("cannot inspect RemoteMCPServer %s credential reference: %w", opt.Server, err)
		}
		matches := 0
		for _, header := range server.Spec.HeadersFrom {
			if strings.EqualFold(header.Name, "Authorization") {
				if header.ValueFrom.Type != "Secret" || header.ValueFrom.Name != opt.Secret || header.ValueFrom.Key != "api-key" {
					return fmt.Errorf("RemoteMCPServer %s Authorization does not reference Secret %s/%s key api-key; nothing issued", opt.Server, opt.SecretNamespace, opt.Secret)
				}
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("RemoteMCPServer %s must have exactly one Authorization reference to Secret %s/%s key api-key; nothing issued", opt.Server, opt.SecretNamespace, opt.Secret)
		}
	}

	// What was true before the credential lands, so a verdict kagent reached
	// before it is never read as an answer about it. The baseline carries the
	// seam's CURRENT verdict time as the API server recorded it, which is what
	// lets the check afterwards prove a change without trusting kmx's clock
	// and the cluster's to agree (seamverdict.go).
	var baseline seamBaseline
	if seamInstalled {
		baseline = a.seamVerdictBaseline(config_kagentNamespace, opt.Server, a.timeNow())
	}
	if err := a.session(func(c *admin.Client) error {
		if err := a.issueCredential(c, opt.Credential, GovernOptions{
			Agent:           opt.Agent,
			Secret:          opt.Secret,
			SecretNamespace: opt.SecretNamespace,
			Command:         "kmx tools govern",
		}, false); err != nil {
			return err
		}
		return a.setToolAllowlist(c, opt.Credential, tools)
	}); err != nil {
		return err
	}

	// Both seams serve TLS under an authority the plane mints for itself,
	// so whatever holds this credential also needs the certificate to
	// verify against — and it needs it where it runs. The seam applied
	// below names the same Secret in the agent namespace, and kagent
	// refuses a RemoteMCPServer whose named Secret is absent, reporting
	// Accepted=false on a fault that has nothing to do with the credential.
	if seamInstalled {
		if err := a.publishPlaneAuthority(config_kagentNamespace); err != nil {
			return err
		}
	}
	if opt.SecretNamespace != config_kagentNamespace || !seamInstalled {
		if err := a.publishPlaneAuthority(opt.SecretNamespace); err != nil {
			return err
		}
	}

	if !seamInstalled {
		// Everything left is kagent reconciling a CRD this cluster does
		// not have. What makes a call governed is written and live.
		a.notef("")
		a.notef("This cluster has no kagent: the CRD %s is not installed, so there is no", remoteMCPServerCRD)
		a.notef("RemoteMCPServer to accept and no Agent to repoint. The credential, its allowlist and")
		a.notef("the authority to verify the seam against are written — that is the whole interface.")
		a.notef("Point your runtime at %s, where <upstream> is",
			scaffold.GatewayURL("<upstream>"))
		a.notef("the name `kmx tools add` onboarded the server under — never the server's own address —")
		a.notef("with the token in Secret %s/%s (key api-key) as `Authorization: Bearer <token>`,",
			opt.SecretNamespace, opt.Secret)
		a.notef("verifying against %s/%s (key %s). See docs/foreign-runtime.md.",
			opt.SecretNamespace, config.PlaneCASecret, config.PlaneCAKey)
		// Every flag that named a kagent object is called out, not just
		// the first. A typo in one of these would otherwise report success
		// while the thing it named was never looked for.
		if opt.Agent != config.DefaultToolsAgent {
			a.notef("--agent %q was not honoured: repointing an Agent is kagent's, and there is none here.",
				opt.Agent)
		}
		if opt.Server != config.DefaultToolServer {
			a.notef("--server %q was not looked for either: a RemoteMCPServer is the object this cluster",
				opt.Server)
			a.notef("  cannot hold. The credential is not scoped to it — the allowlist is per-credential,")
			a.notef("  and the upstream a call reaches is the name in its URL.")
		}
		return nil
	}

	// The committed seam is kmx's to apply; a scaffolded one was applied
	// by `kmx tools add` and its file is the operator's artifact.
	if opt.Server == config.DefaultToolServer {
		if err := a.apply("kaimahi-tools.yaml"); err != nil {
			return err
		}
	}
	// Accepted, not Ready: a RemoteMCPServer reports that it reached the
	// upstream and discovered its tools. Patching the agent before that
	// would point it at a server with no discovered tools, and kagent
	// wires discovered ∩ toolNames — an empty intersection is an agent
	// with no tools at all, which looks exactly like a policy denial.
	//
	// And it has to be a verdict reached AFTER the credential above was
	// written. `kubectl wait --for=...Accepted=True` is satisfied by a
	// cached True from before it and returns instantly, which is how a
	// credential that cannot be used reported as working for minutes
	// (seamverdict.go).
	verdict, err := a.waitForSeamVerdict(config_kagentNamespace, opt.Server, baseline)
	if err != nil {
		return err
	}
	switch verdict.State {
	case verdictRejected:
		return fmt.Errorf("kagent checked the %s seam against the credential just written and was refused: %s\n"+
			"  The credential and allowlist ARE written; the agent has not been repointed.%s",
			opt.Server, verdict.Message, a.certificateNote(verdict.Message))
	case verdictUnknown:
		// The stale verdict this branch carries can itself be a trust
		// failure, and it is worth naming here for the same reason as
		// above: kagent's own message for one says only that something
		// could not connect.
		a.notef("The %s seam's status is %s\n"+
			"  Repointing the agent anyway: the credential and allowlist are written and correct, and\n"+
			"  a seam kagent has not re-checked is not a seam known to be broken.%s",
			opt.Server, verdict.Line(a.timeNow()), a.certificateNote(verdict.Message))
	}
	if err := a.patchAgentTools(opt.Server, opt.Agent, tools); err != nil {
		return err
	}
	if err := a.waitSwitched(opt.Agent); err != nil {
		return err
	}
	return a.waitAgentReady(opt.Agent)
}

// remoteMCPServerCRD is the CustomResourceDefinition a cluster must carry
// before a RemoteMCPServer can be applied to it.
const remoteMCPServerCRD = "remotemcpservers.kagent.dev"

// kagentSeamInstalled reports whether this cluster can accept a
// RemoteMCPServer at all.
//
// The question is asked positively, of a CRD by name, because the obvious
// read does not answer it: a cluster with no kagent answers `get
// remotemcpserver` with "the server doesn't have a resource type", which
// isNotFound deliberately does NOT read as absence (notfound_test.go). A
// `get crd <name>` is a read of a resource every cluster has, so its
// NotFound means one thing only.
//
// A cluster that could not be asked is an error, never an absence.
// Treating an unreachable API server or an RBAC denial as "no kagent
// here" would half-onboard a cluster and report success.
func (a *App) kagentSeamInstalled() (bool, error) {
	if _, err := a.kubectlCapture("get", "crd", remoteMCPServerCRD, "-o", "name"); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("cannot tell whether this cluster has the kagent CRDs "+
			"(refusing to guess): %w", err)
	}
	return true, nil
}

// requireNamespace refuses before anything is written when the namespace
// a Secret is bound for does not exist.
//
// The credential's token is shown once. Minting it and THEN failing to
// write the Secret leaves a live credential nobody holds and a recovery
// that is a hand-written DELETE against the plane's database — which is
// what happens when the Secret namespace defaults to `kagent` on a
// cluster that has none.
func (a *App) requireNamespace(namespace, flag string) error {
	if _, err := a.kubectlCapture("get", "namespace", namespace, "-o", "name"); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("namespace %q does not exist, and it is where the credential's Secret would go.\n"+
				"  Nothing has been issued — the token is shown once, so this is refused before it is minted.\n"+
				"  Name the namespace your runtime reads its Secret from:\n"+
				"    %s <your namespace>", namespace, flag)
		}
		return fmt.Errorf("cannot tell whether namespace %q exists (refusing to guess): %w", namespace, err)
	}
	return nil
}

// preflightToolServer refuses early when the RemoteMCPServer this is
// meant to govern against does not exist, and names the command that
// creates it. Only a genuine NotFound is treated as absence: an
// unreachable API server or an RBAC denial must not be reported as
// "you forgot to apply it".
func (a *App) preflightToolServer(server string) error {
	_, err := a.kubectlCapture("-n", config_kagentNamespace, "get",
		"remotemcpserver", server, "-o", "name")
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	return fmt.Errorf("no RemoteMCPServer %q in namespace %s.\n"+
		"  Apply the seam before governing; no credential or allowlist was changed.\n"+
		"  If you scaffolded with --no-apply or --dry-run, apply the manifest first:\n"+
		"    kubectl --context %s apply -f upstreams/<name>.yaml",
		server, config_kagentNamespace, a.Cfg.KubeContext)
}

// UngovernTools restores the original wiring — direct to the chart-managed
// tool server, ungoverned. Only tool wiring changes; model governance,
// instructions, deployment settings and other agent customizations survive.
//
// It ends at `wait_switched`, with no Ready wait, exactly as
// `make ungovern-tools` does: the committed agent is the one `kmx up`
// created and already proved Ready, so the question here is only whether
// the OLD pod is gone. That is what wait_switched answers, and it is the
// answer that matters — an invoke landing on a draining pod would still ride
// the gateway.
func (a *App) UngovernTools(opt ToolsOptions) error {
	opt = a.toolsDefaults(opt)
	// Only this agent has a known committed direct tool selection.
	if opt.Agent != config.DefaultToolsAgent {
		return fmt.Errorf("kmx tools ungovern restores the committed agent %q, not %q.\n"+
			"  There is no committed ungoverned form of %q to restore; repoint it yourself:\n"+
			"    kubectl --context %s -n %s edit agents.kagent.dev %s",
			config.DefaultToolsAgent, opt.Agent, opt.Agent, shellArg(a.Cfg.KubeContext), config.DefaultNamespace, opt.Agent)
	}
	if err := a.Guard(fmt.Sprintf("return agent %q to the ungoverned tool server", opt.Agent),
		a.operationCommand("tools", "ungovern")); err != nil {
		return err
	}
	if err := a.patchAgentTools("kagent-tool-server", opt.Agent, []string{"k8s_get_resources"}); err != nil {
		return err
	}
	return a.waitSwitched(opt.Agent)
}

// AllowTools replaces a credential's tool allowlist (`make tool-allow`).
func (a *App) AllowTools(credential, list string) error {
	if err := admin.ValidCredentialName(credential); err != nil {
		return err
	}
	tools, err := admin.ParseToolList(list)
	if err != nil {
		return err
	}
	if err := a.Guard(fmt.Sprintf("replace the tool allowlist for credential %q with [%s]", credential, quotedList(tools)),
		a.operationCommand("tools", "allow", list, "--credential", credential)); err != nil {
		return err
	}
	return a.session(func(c *admin.Client) error {
		return a.setToolAllowlist(c, credential, tools)
	})
}

// ToolAllowlist prints what a credential may call (`make tool-allowlist`).
// A read: unguarded.
func (a *App) ToolAllowlist(credential string) error {
	return a.session(func(c *admin.Client) error { return c.ToolAllowlist(a.Out, credential) })
}

// setToolAllowlist writes the allowlist and says what it means. The second
// note is not decoration: enforcement
// is immediate, but what an AGENT can see only catches up on kagent's next
// RemoteMCPServer reconcile, and an operator who does not know that reads
// the lag as the allowlist not having taken.
func (a *App) setToolAllowlist(c *admin.Client, credential string, tools []string) error {
	if err := c.SetToolAllowlist(credential, tools); err != nil {
		return err
	}
	a.notef("Tool allowlist for %q: [%s] (enforced on tools/call, projected on tools/list).",
		credential, quotedList(tools))
	// Said for both runtimes rather than for kagent alone. On a cluster
	// with no kagent the old sentence sent an operator looking for a
	// reconcile that cannot happen, and a client that simply lists again
	// is the general case anyway.
	a.notef("A client sees the projection on its next tools/list; where kagent runs, it re-discovers")
	a.notef("the projection on its next RemoteMCPServer reconcile. Enforcement is immediate either way.")
	return nil
}

// quotedList renders the JSON array's own contents, so what is echoed is what
// was sent.
func quotedList(tools []string) string {
	quoted := make([]string, 0, len(tools))
	for _, t := range tools {
		quoted = append(quoted, `"`+t+`"`)
	}
	return strings.Join(quoted, ", ")
}

// patchAgentTools points an agent at the governed RemoteMCPServer with an
// explicit toolNames selection — the Makefile's TOOLNAMES_JSON patch.
//
// An EMPTY selection is passed through as an empty array, not omitted:
// `make tool-allow TOOLS=-` means nothing is callable, and an agent whose
// toolNames key vanished would fall back to every discovered tool.
func (a *App) patchAgentTools(server, agent string, tools []string) error {
	if tools == nil {
		tools = []string{}
	}
	names, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	name, err := json.Marshal(server)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(
		`{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":%s,"toolNames":%s}}]}}}`,
		name, names)
	return a.kubectlRun("-n", config_kagentNamespace, "patch", "agents.kagent.dev", agent, "--type", "merge", "-p", patch)
}

// toolsDefaults fills the knobs the operator did not name. The credential
// comes from the RESOLVED configuration, not from the constant, so
// CRED_TOOLS in the environment reaches `kmx tools` the same way it reaches
// `make govern-tools`.
func (a *App) toolsDefaults(o ToolsOptions) ToolsOptions {
	if o.Server == "" {
		o.Server = config.DefaultToolServer
	}
	if o.Credential == "" {
		o.Credential = a.Cfg.ToolsCredential
	}
	if o.Credential == "" {
		o.Credential = config.DefaultToolsCredential
	}
	if o.Agent == "" {
		o.Agent = config.DefaultToolsAgent
	}
	if o.Secret == "" {
		o.Secret = config.DefaultToolsSecret
	}
	if o.SecretNamespace == "" {
		o.SecretNamespace = config.DefaultNamespace
	}
	if o.Tools == "" {
		o.Tools = config.DefaultTools
	}
	return o
}
