package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

// QuickstartAgent is the Orka Provider and Agent this command creates.
//
// It is fixed rather than a flag for the same reason there is no --agent:
// quickstart deploys one known bundle and then asks THAT agent the question.
// A name that could differ from the thing being deployed would let the
// command answer about something it did not create. Authoring your own is
// `kmx agent create`; asking any agent anything is `kmx agent chat`.
const QuickstartAgent = "hello-world-agent"

// QuickstartOptions configure the shortest path to a first answer.
type QuickstartOptions struct {
	// Output is "text" for a person or "json" for whatever is driving.
	Output string
	Task   string
}

// QuickstartTool is one dependency, and where it came from.
type QuickstartTool struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source"`
}

// QuickstartResult is what `--output json` prints: enough for an agent in a
// harness to decide what to do next without parsing prose.
type QuickstartResult struct {
	OK       bool   `json:"ok"`
	Context  string `json:"context"`
	Cluster  string `json:"cluster"`
	Agent    string `json:"agent"`
	Manifest string `json:"manifest"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	// Governed retains the original boolean wire contract: governance enabled
	// by this invocation, not an observation of existing cluster governance.
	// It is always false: quickstart does not enable governance. A rerun may
	// preserve existing governance; callers must not infer its absence here.
	Governed       bool             `json:"governed"`
	Tools          []QuickstartTool `json:"tools"`
	ElapsedSeconds float64          `json:"elapsed_seconds"`
	Next           []string         `json:"next"`
}

// quickstartStep is one addressable phase of the first-answer journey.
type quickstartStep struct {
	name string
	fn   func() error
}

// Quickstart is the whole distance from a machine with a container engine to
// an agent answering a question, in one command.
//
// It is not a new journey. Every step is `kmx up`'s, in `kmx up`'s order,
// with the same waits and the same fail-closed checks — what it ADDS is the
// one thing `up` deliberately does not: a fixed Orka Agent and a question
// put to it. The runtime is Orka; nothing on this path installs, reads or
// depends on the retired runtime.
//
// It is deterministic and non-interactive on purpose. There is no model
// picker and no prompt: the same command on the same machine produces the
// same cluster, the same Provider and the same Agent, which is what lets an
// unattended caller rerun it and compare. Choosing your own model and
// authoring your own agent is `kmx quickstart-wizard`.
//
// This command does not enable governance; existing governance may survive
// a rerun and is not assessed here.
func (a *App) Quickstart(opt QuickstartOptions) error {
	started := a.timeNow()
	task := opt.Task
	if task == "" {
		task = config.DefaultTask
	}
	asJSON := false
	switch strings.ToLower(strings.TrimSpace(opt.Output)) {
	case "", "text":
	case "json":
		asJSON = true
	default:
		return fmt.Errorf("unknown --output %q — expected text or json", opt.Output)
	}

	// Machine-readable means the WHOLE stream, not the last line of it.
	// Every command kmx shells out to writes its own stdout to kmx's
	// (cmd/kmx wires Runner.Stdout to it), so `kind create` and every
	// `kubectl wait` would land in front of the JSON and the caller would
	// get "Expecting value: line 1 column 1". Under --output json those go
	// to stderr with everything else humans read, leaving stdout carrying
	// exactly one document. `kmx metrics` already draws this line for the
	// same reason.
	if asJSON {
		a.Run.Stdout = a.Err
	}
	if err := a.validateKindTarget(); err != nil {
		return err
	}

	// Equip the machine first. Everything after this point assumes kind and
	// kubectl are runnable, and the whole point of the command is that a
	// machine which had neither still gets there. Helm is not on this path:
	// the Orka runtime is a pinned manifest, not a chart.
	if err := a.preflight(depKind, depKubectl, a.engineDependency()); err != nil {
		return err
	}
	if len(a.provisioned) > 0 && !asJSON {
		a.notef("Tools this run is using:")
		toolchain.Report(a.Err, a.provisioned)
	}

	if err := a.GuardCreateIn("create a local cluster and a first agent", "kmx quickstart", OrkaPathNamespaces); err != nil {
		return err
	}

	steps := a.quickstartSteps()
	total := len(steps) + 1
	for i, step := range steps {
		if err := a.runPhase(phase{current: i + 1, total: total, name: step.name}, step.fn); err != nil {
			return err
		}
	}

	var answer string
	if err := a.runPhase(phase{current: total, total: total, name: "Ask " + QuickstartAgent + " a question"}, func() error {
		var err error
		answer, err = a.quickstartAnswer(task)
		return err
	}); err != nil {
		return err
	}

	result := a.quickstartResult(task, answer, started)
	if asJSON {
		encoder := json.NewEncoder(a.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Fprintf(a.Out, "\n%s\n", safeTerminal(answer))
	a.complete("An agent answered", started)
	a.notef("\n%s  This command does not enable governance.\n"+
		"Existing governance is not assessed by quickstart. To configure it:\n"+
		"  %s  # the metering proxy and its ledger\n"+
		"  %s  # put an application's model traffic on the seam (docs/migrate.md)",
		a.presenter().Warning("GOVERNANCE"), result.Next[3], result.Next[4])
	a.quickstartNext(cliui.New(a.Err), result)
	return nil
}

// quickstartSteps is the supported clean-machine sequence, in order.
func (a *App) quickstartSteps() []quickstartStep {
	return []quickstartStep{
		{"Prepare kind cluster", a.stepCluster},
		{"Deploy Ollama", a.stepOllama},
		{"Pull model " + a.Cfg.Model, a.stepModel},
		{"Install Orka " + OrkaVersion, a.stepOrka},
		{"Deploy the " + QuickstartAgent + " agent", a.stepQuickstartAgent},
	}
}

func (a *App) quickstartResult(question, answer string, started time.Time) QuickstartResult {
	result := QuickstartResult{
		OK:      true,
		Context: a.Cfg.KubeContext,
		Cluster: a.Cfg.KindCluster,
		Agent:   QuickstartAgent,
		Manifest: fmt.Sprintf("Orka Provider/Agent %s in %s, generated by kmx (authoring: docs/kmx.md#kmx-agent-create)",
			QuickstartAgent, OrkaNamespace),
		Question:       question,
		Answer:         answer,
		Governed:       false,
		Next:           a.quickstartFollowups(),
		ElapsedSeconds: a.timeNow().Sub(started).Seconds(),
	}
	for _, t := range a.provisioned {
		result.Tools = append(result.Tools, QuickstartTool{Name: t.Name, Version: t.Version, Source: string(t.Source)})
	}
	return result
}

// quickstartCreateOptions is the one bundle quickstart deploys.
//
// The Provider reuses the placeholder key `kmx orka install` already wrote
// for its own keyless Provider rather than minting a second one: the
// in-cluster endpoint needs no credential, and Orka's Provider schema
// requires a secretRef whether or not the endpoint reads it.
func (a *App) quickstartCreateOptions() CreateOptions {
	port := a.quickstartResultPort
	if port == "" {
		port = "19180"
	}
	return CreateOptions{
		Name:                 QuickstartAgent,
		Namespace:            OrkaNamespace,
		Description:          "The agent kmx quickstart deploys to answer a first question",
		ProviderType:         "openai",
		Model:                a.Cfg.Model,
		BaseURL:              orkaDefaultModelURL,
		Secret:               orkaDefaultProvider + "-provider-key",
		SecretKey:            "api-key",
		ResultServiceAccount: orkaResultAccount,
		OrkaAPIService:       "orka-api",
		ResultPort:           port,
	}
}

// stepQuickstartAgent creates the fixed Provider and Agent, or reuses them.
//
// Reuse is EXACT-MATCH ONLY. A live Provider or Agent whose spec differs
// from the one quickstart would write is somebody's deliberate change — an
// edited endpoint, a different model, a hand-applied bundle — so this stops
// rather than overwrite it, exactly as a rerun over a full legacy release
// used to preserve that release. A half-finished run resumes: whichever of
// the two already matches is kept, and the other is created.
func (a *App) stepQuickstartAgent() error {
	ctx, cancel := context.WithTimeout(a.operationContext(), 10*time.Minute)
	defer cancel()
	opt := a.quickstartCreateOptions()
	bundle, err := createOrkaBundle(opt)
	if err != nil {
		return err
	}
	if err := a.orkaProviderSecretPresent(ctx, opt.Namespace, opt.Secret, opt.SecretKey); err != nil {
		return err
	}
	for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
		id, err := a.matchingOrkaResource(ctx, opt.Namespace, doc)
		if err != nil {
			if errors.Is(err, errOrkaConfigurationDrift) {
				return fmt.Errorf("%w; keep the drifted resource and run `kmx agent create` under a different name, or delete the fixed %s so quickstart recreates it", err, QuickstartAgent)
			}
			return err
		}
		if id == nil {
			created, err := a.createOrkaObject(ctx, opt.Namespace, doc)
			if err != nil {
				return err
			}
			a.notef("Created %s/%s (UID %s); waiting for current-generation Ready.", created.Kind, created.Name, created.UID)
			id = &created
		} else {
			a.notef("Reusing matching %s/%s (UID %s).", id.Kind, id.Name, id.UID)
		}
		if err := a.waitOrkaReady(ctx, opt.Namespace, *id); err != nil {
			return err
		}
	}
	return nil
}

// quickstartAnswer creates a FRESH Task and returns its retrieved reply.
//
// Fresh every run, never reused: an existing completed Task holds an earlier
// run's answer, and reporting it as this run's would turn "the agent
// answered" into "the agent answered once, some time ago". The reply must be
// non-blank after sanitisation — a Task that completed with nothing readable
// in it is a failed run, not an answer.
func (a *App) quickstartAnswer(task string) (string, error) {
	// Eight minutes, not ten: the result token is minted for ten and the
	// session refuses to start unless the grant outlasts the deadline by
	// thirty seconds. A ten-minute deadline here would refuse every run.
	ctx, cancel := context.WithTimeout(a.operationContext(), 8*time.Minute)
	defer cancel()
	opt := a.quickstartCreateOptions()
	opt.Task = task
	session, err := a.openOrkaResultSession(ctx, opt)
	if err != nil {
		return "", err
	}
	defer session.close()
	return a.runQuickstartOrkaTaskProfile(ctx, QuickstartAgent, OrkaNamespace, task, nil, nil, session)
}

// quickstartFollowups are the commands this cluster can actually run next.
//
// The chat follow-up carries --interactive because Orka chat has no one-shot:
// `kmx agent chat --runtime orka` without it is refused by name, so printing
// the shorter command would end the first answer with an instruction that
// fails.
func (a *App) quickstartFollowups() []string {
	return []string{
		a.operationCommand("agent", "chat", QuickstartAgent, "--interactive", "--runtime", "orka", "--namespace", OrkaNamespace, "ask it something else"),
		a.operationCommand("agent", "create"),
		a.operationCommand("orka", "status"),
		a.operationCommand("plane"),
		a.operationCommand("migrate", "<deployment>", "--namespace", "<ns>", "--model", orkaDefaultProvider+"/"+a.Cfg.Model),
	}
}

func (a *App) quickstartNext(ui cliui.Output, result QuickstartResult) {
	down := a.operationCommand("down")
	if ui.Rich() {
		a.notef("\n%s", ui.Actions("Next", []cliui.Action{
			{Label: "Ask another question", Command: result.Next[0]},
			{Label: "Author your own Orka Agent", Command: result.Next[1], Detail: "docs/kmx.md#kmx-agent-create"},
			{Label: "Inspect the Orka runtime", Command: result.Next[2], Detail: "what is installed, and what it can resolve"},
			{Label: "Delete this cluster", Command: down, Detail: "delete the cluster and everything in it"},
		}))
	} else {
		a.notef("\nNEXT  %s  # ask it something else\n"+
			"      %s  # author your own agent; docs/kmx.md#kmx-agent-create\n"+
			"      %s  # what is installed, and what it can resolve\n"+
			"      %s  # delete the cluster and everything in it", result.Next[0], result.Next[1], result.Next[2], down)
	}
}

// shellArg quotes a POSIX shell argument, not a Go string literal.
func shellArg(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// operationCommand pins follow-up actions to the context this operation used.
func (a *App) operationCommand(args ...string) string {
	parts := []string{"kmx", "--context", shellArg(a.Cfg.KubeContext)}
	if len(args) > 0 && (args[0] == "up" || args[0] == "plane" || args[0] == "down") {
		parts = append([]string{"KIND_CLUSTER=" + shellArg(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + shellArg(a.Cfg.ContainerEngine)}, parts...)
	}
	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}
	return strings.Join(parts, " ")
}
