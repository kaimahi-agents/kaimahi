package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// CreateOptions are `kmx agent create`'s flags. There is deliberately no
// option here that could carry a credential, in any form — no flag, no
// environment variable, no file. The generator emits Secret REFERENCES; a
// scaffolder that can take a key is a scaffolder that can leak one into a
// file you are about to commit.
type CreateOptions struct {
	Name         string
	Namespace    string
	Description  string
	ModelConfig  string // empty: resolved against the cluster
	Instructions string // path to a file
	// InstructionText is populated by the interactive wizard and passes
	// through the same credential-shape and YAML safety checks.
	InstructionText string
	Tools           string // server:tool1,tool2
	Out             string // empty: agents/<name>.yaml
	NoApply         bool
	DryRun          bool
	Image           string // non-empty: a BYO agent serving A2A on :8080
	Isolation       string // placement profile, or "none"
	// RunAsUser is the numeric UID a bring-your-own image runs as, or the
	// literal "root" to say so on purpose. It is asked for rather than
	// guessed: see scaffold.ParseRunAsUser.
	RunAsUser string
}

type serverCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	// LastTransitionTime is when kagent reached this verdict. It is what
	// separates a live answer from a cached one about a credential that has
	// since been replaced (seamverdict.go).
	LastTransitionTime string `json:"lastTransitionTime"`
	ObservedGeneration int64  `json:"observedGeneration"`
}

// CreateAgent scaffolds an agent, then applies it.
//
// The YAML is the artifact and it is written first, so the operator ends up
// with the file whether or not the cluster accepts it. Applying goes through
// the same context guard as every other mutation.
func (a *App) CreateAgent(opt CreateOptions) error {
	started := a.timeNow()
	if opt.Out == "-" {
		opt.NoApply = true
	}
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply and --dry-run cannot be used together")
	}
	if err := refuseFlagsBYODrops(opt); err != nil {
		return err
	}
	if opt.Isolation != "" && opt.Image == "" {
		// The generator makes this check too, but it can only see the
		// RESOLVED placement — and "none" resolves to no placement at all,
		// so it arrived there indistinguishable from a flag nobody passed.
		// `--isolation none` was therefore the one spelling that slipped
		// through the rule the other spellings are refused by. Checked here,
		// where the flag as typed is still visible.
		return fmt.Errorf("--isolation needs --image: placement applies to a BYO agent's pod")
	}
	namespace := opt.Namespace
	if namespace == "" {
		namespace = config.DefaultNamespace
	}
	path := opt.Out
	if path == "" {
		path = filepath.Join("agents", opt.Name+".yaml")
	}
	total := 3
	if opt.NoApply {
		total = 1
	} else if opt.DryRun {
		total = 2
	}
	var tools *scaffold.ToolWiring
	var modelConfig string
	if err := a.runPhase(phase{current: 1, total: total, name: "Generate agent manifest"}, func() error {
		if err := scaffold.ValidateName(opt.Name); err != nil {
			return err
		}
		var err error
		tools, err = scaffold.ParseTools(opt.Tools)
		if err != nil {
			return err
		}
		instructions := opt.InstructionText
		if opt.Instructions != "" {
			body, err := os.ReadFile(opt.Instructions)
			if err != nil {
				return fmt.Errorf("cannot read the instructions file: %w", err)
			}
			instructions = string(body)
		}
		if !opt.NoApply {
			if err := a.preflight(depKubectl); err != nil {
				return err
			}
		}
		governed := false
		modelConfig, governed, err = a.resolveModelConfig(opt, namespace)
		if err != nil {
			return err
		}
		placement, err := scaffold.ParsePlacement(opt.Isolation)
		if err != nil {
			return err
		}
		identity, err := scaffold.ParseRunAsUser(opt.RunAsUser, opt.Image)
		if err != nil {
			return err
		}
		// A bring-your-own image has no modelConfig and no tools to point
		// at, so the seams a declarative agent gets by reference are
		// carried across as environment instead.
		var governance []scaffold.EnvVar
		if opt.Image != "" {
			governance = scaffold.GovernanceEnv(governed)
		}
		document, err := scaffold.Generate(scaffold.Spec{
			Name: opt.Name, Namespace: namespace, Description: opt.Description,
			ModelConfig: modelConfig, Instructions: instructions, Tools: tools, Governed: governed,
			Image: opt.Image, Placement: placement, Governance: governance, Identity: identity,
		})
		if err != nil {
			return err
		}
		if path == "-" {
			fmt.Fprint(a.Out, document)
		} else {
			if err := scaffold.WriteNew(path, document); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "wrote %s\n", path)
		}
		if opt.Image != "" {
			a.noteBYO(opt.Image, governance, placement, identity)
		}
		if !governed {
			a.notef("WARNING: %q is ungoverned — no budget, no ledger, no audit in front of it.\n"+
				"         `kmx plane` then `kmx govern` puts the plane in front of an agent.", modelConfig)
		}
		if opt.Image != "" {
			// NOT the declarative report. A BYO manifest carries no
			// toolNames, so "agent allowlist only" would name an allowlist
			// that is not in the document — a governance claim about a
			// control that does not exist.
			a.notef("CAPABILITIES\n  Tools: whatever the image reaches for; kmx cannot enumerate them.\n" +
				"  Governance: the gateway is the only control, and only for calls the\n" +
				"  image actually sends through KAIMAHI_MCP_URL. `kmx audit tool` is the evidence.")
		} else if tools == nil {
			a.notef("CAPABILITIES\n  Tools: none\n  Add later: kmx agent create <name> --tools <server>:<tool>[,<tool>...]")
		} else {
			a.notef("CAPABILITIES\n  Tools: %s via %s\n  Governance: agent allowlist only; no gateway audit until `kmx tools govern`", strings.Join(tools.Tools, ", "), tools.Server)
		}
		return nil
	}); err != nil {
		return err
	}

	if opt.NoApply {
		label := "Agent manifest written; not applied"
		if path == "-" {
			label = "Agent manifest printed; not applied"
		}
		a.complete(label, started)
		if path != "-" {
			a.notef("\nNEXT  Review it, then:\n  kubectl --context %s apply -f %s", a.Cfg.KubeContext, path)
		}
		return nil
	}

	action := "apply agent " + opt.Name + " from " + path
	command := "kmx agent create " + opt.Name
	phaseName := "Validate and apply agent"
	if opt.DryRun {
		action = "server-side validate agent " + opt.Name + " from " + path
		command += " --dry-run"
		phaseName = "Validate agent against cluster"
	}
	fmt.Fprintln(a.Err)
	if err := a.Guard(action, command); err != nil {
		return err
	}
	if err := a.runPhase(phase{current: 2, total: total, name: phaseName}, func() error {
		if err := a.preflightModelConfig(modelConfig, namespace); err != nil {
			return err
		}
		if err := a.preflightTools(tools, namespace); err != nil {
			return err
		}
		if opt.DryRun {
			return a.kubectlRun("apply", "--dry-run=server", "-f", path)
		}
		return a.kubectlRun("apply", "-f", path)
	}); err != nil {
		return err
	}
	if opt.DryRun {
		a.complete("Agent manifest validated; not applied", started)
		return nil
	}
	if err := a.runPhase(phase{current: 3, total: total, name: "Wait for agent Ready"}, func() error {
		return a.waitAgentReady(opt.Name)
	}); err != nil {
		return err
	}
	a.complete(fmt.Sprintf("Agent %q ready", opt.Name), started)
	a.notef("\nNEXT  kmx agent chat --interactive %s", opt.Name)
	return nil
}

func (a *App) preflightTools(tools *scaffold.ToolWiring, namespace string) error {
	if tools == nil {
		return nil
	}
	raw, err := a.kubectlCapture("-n", namespace, "get", "remotemcpserver", tools.Server, "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read RemoteMCPServer %q in namespace %s: %w", tools.Server, namespace, err)
	}
	var server struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			ObservedGeneration int64             `json:"observedGeneration"`
			Conditions         []serverCondition `json:"conditions"`
			DiscoveredTools    []struct {
				Name string `json:"name"`
			} `json:"discoveredTools"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &server); err != nil {
		return fmt.Errorf("RemoteMCPServer %q returned invalid JSON: %w", tools.Server, err)
	}
	discovered := map[string]bool{}
	for _, tool := range server.Status.DiscoveredTools {
		discovered[tool.Name] = true
	}
	return validateToolServer(tools, server.Metadata.Generation, server.Status.ObservedGeneration,
		server.Status.Conditions, discovered)
}

func validateToolServer(tools *scaffold.ToolWiring, generation, observed int64, conditions []serverCondition, discovered map[string]bool) error {
	if generation == 0 || observed != generation {
		return fmt.Errorf("RemoteMCPServer %q is still reconciling (generation %d, observed %d)", tools.Server, generation, observed)
	}
	accepted := false
	message := ""
	for _, condition := range conditions {
		if condition.Type == "Accepted" {
			if condition.ObservedGeneration != generation {
				continue
			}
			accepted = condition.Status == "True"
			message = condition.Message
		}
	}
	if !accepted {
		detail := ""
		if message != "" {
			detail = ": " + message
		}
		return fmt.Errorf("RemoteMCPServer %q is not Accepted%s", tools.Server, detail)
	}
	var missing []string
	for _, tool := range tools.Tools {
		if !discovered[tool] {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("RemoteMCPServer %q has not discovered allowlisted tool(s): %s\n  Available tools: %s",
			tools.Server, strings.Join(missing, ", "), strings.Join(sortedBoolKeys(discovered), ", "))
	}
	return nil
}

func sortedBoolKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// resolveModelConfig decides what the agent thinks with.
//
// Governed by default WHERE A PLANE EXISTS: if the plane's governed preset is
// on the cluster, the scaffolded agent is metered, budgeted and ledgered from
// its first call. On a fresh `kmx up` cluster there is no plane (`kmx up`
// does not deploy one), so the keyless in-cluster preset is used and the
// ungoverned warning is printed. An explicit --model always wins.
// An unreachable cluster is NOT "no plane". Getting that wrong is the whole
// failure mode this project exists to prevent: a governed agent silently
// scaffolded onto the keyless preset, with no budget, no ledger and no audit,
// because an API call blipped. So the read distinguishes a genuine NotFound
// from any other failure, and only the former means "no plane here".
func (a *App) resolveModelConfig(opt CreateOptions, namespace string) (string, bool, error) {
	if opt.ModelConfig != "" {
		return opt.ModelConfig, opt.ModelConfig == config.GovernedModelConfig, nil
	}
	governed, err := a.modelConfigExists(config.GovernedModelConfig, namespace)
	switch {
	case err != nil && !opt.NoApply:
		return "", false, err
	case err != nil:
		// Scaffolding to a file without applying is a legitimate offline
		// action; say plainly that the governance choice was a guess.
		a.notef("NOTE: could not ask the cluster whether a governed preset exists (%v).\n"+
			"      Scaffolding against %q — check the modelConfig before you apply this.",
			err, config.KeylessModelConfig)
		return config.KeylessModelConfig, false, nil
	case governed:
		return config.GovernedModelConfig, true, nil
	}
	return config.KeylessModelConfig, false, nil
}

func (a *App) modelConfigExists(name, namespace string) (bool, error) {
	_, err := a.kubectlCapture("-n", namespace, "get", "modelconfig", name, "-o", "name")
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("cannot read modelconfig %q in namespace %s — refusing to guess whether this cluster has a governance plane: %w",
			name, namespace, err)
	}
}

// preflightModelConfig checks the ModelConfig before applying.
//
// A missing ModelConfig is ADMITTED by the API server and then fails to
// reconcile in silence: the Agent exists, never becomes Ready, and nothing
// says why. Check first, and print the fix (#16 hit exactly this).
func (a *App) preflightModelConfig(name, namespace string) error {
	exists, err := a.modelConfigExists(name, namespace)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	extra := ""
	if name == config.KeylessModelConfig {
		extra = "\n  On a fresh machine that is `kmx up`."
	}
	if name == config.GovernedModelConfig {
		extra = "\n  The governed presets come with the plane: `kmx plane` then `kmx govern`."
	}
	return fmt.Errorf("ModelConfig %q does not exist in namespace %s.\n"+
		"  The API server would accept the Agent and then never reconcile it, silently.\n"+
		"  Existing presets:  kubectl --context %s -n %s get modelconfigs%s",
		name, namespace, a.Cfg.KubeContext, namespace, extra)
}

// RefuseUnknownAgentVerb keeps update and delete in kubectl rather than
// growing weaker copies after kmx's focused list/create/chat surface.
func RefuseUnknownAgentVerb(verb, kubeContext string) error {
	ctx := ""
	if kubeContext != "" {
		ctx = " --context " + kubeContext
	}
	return fmt.Errorf("kmx: unknown command 'agent %s'.\n"+
		"Use `kmx agent list`, `kmx agent create`, `kmx agent edit`, or `kmx agent chat`.\n"+
		"Direct live-resource editing and deletion remains kubectl's job:\n"+
		"  kubectl%s -n kagent edit agent <name>\n"+
		"  kubectl%s -n kagent delete agent <name>\n"+
		"  kubectl%s apply -f agents/<name>.yaml",
		verb, ctx, ctx, ctx)
}

// noteBYO says what a bring-your-own agent got and — the part that matters —
// what kmx could not check.
//
// Every line here exists because the image is opaque. A declarative agent's
// governance is a reference the controller resolves and the manifest shows;
// a BYO agent's is an environment variable that only the image can honour,
// and kmx has no way to look inside and see whether it does. The same is
// true of its user and its filesystem. So each of those is stated, not
// implied by silence.
func (a *App) noteBYO(image string, governance []scaffold.EnvVar, placement *scaffold.Placement, identity scaffold.Identity) {
	a.notef("BYO agent: kagent will deploy %s and expect A2A on :8080.\n"+
		"         It has no modelConfig and no tools field — those exist only on\n"+
		"         declarative agents — so the governed seams travel as env instead.", image)
	if len(governance) > 0 {
		a.notef("Injected the governed seams into the pod's env:")
		for _, e := range governance {
			if e.SecretRef != "" {
				a.notef("           %s <- secret %s/%s", e.Name, e.SecretRef, e.Value)
				continue
			}
			a.notef("           %s = %s", e.Name, e.Value)
		}
		a.notef("CONFIGURED, NOT PROVEN: kmx cannot verify the image honours these.\n" +
			"         `kmx ledger` is the evidence — a row there means it did.")
	}
	a.notef("%s", identity.Note)
	if placement != nil {
		a.notef("%s", placement.Note)
	} else {
		a.notef("No placement profile: this pod schedules like any other. `--isolation\n" +
			"         virtual-node` puts it on an ACI virtual node instead.")
	}
}

// refuseFlagsBYODrops rejects the flags a BYO manifest would silently discard.
//
// `spec.byo` has one property, `deployment`. There is no `systemMessage` and
// no `tools`, so `--instructions` and `--tools` do not reach the document at
// all — and `--tools` was worse than inert: the capabilities report printed
// the allowlist back, so kmx claimed a control that was not in the manifest
// it had just written. The same rule the isolation flags follow: a flag that
// quietly does nothing is worse than no flag.
//
// `--model` is NOT refused. It does not reach a BYO document either — the
// image chooses its own model — but it still decides whether the governed
// seams are injected as env, which is a real effect on a real artifact.
func refuseFlagsBYODrops(opt CreateOptions) error {
	if opt.Image == "" {
		return nil
	}
	var dropped []string
	if opt.Tools != "" {
		dropped = append(dropped, "--tools")
	}
	if opt.Instructions != "" || opt.InstructionText != "" {
		dropped = append(dropped, "--instructions")
	}
	if len(dropped) == 0 {
		return nil
	}
	return fmt.Errorf("%s cannot be combined with --image: kagent's Agent CRD puts\n"+
		"systemMessage and tools under `declarative`, and a BYO agent has neither —\n"+
		"the image supplies its own prompt and reaches its own tools. Refusing rather\n"+
		"than dropping them, because a scaffolder that accepted --tools here would\n"+
		"report an allowlist that is not in the manifest it wrote.\n"+
		"  Allowlist a BYO agent's tool calls at the gateway instead: `kmx tools govern`",
		strings.Join(dropped, " and "))
}
