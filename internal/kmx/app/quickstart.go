package app

import (
	"encoding/json"
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
// offline `kmx create`; asking any agent anything is `kmx chat`.
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
	OK          bool   `json:"ok"`
	Context     string `json:"context"`
	Cluster     string `json:"cluster"`
	Agent       string `json:"agent"`
	Manifest    string `json:"manifest"`
	SuiteDigest string `json:"suite_digest,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	Layout      string `json:"layout,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Question    string `json:"question"`
	Answer      string `json:"answer"`
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
// It is not a new journey. Every step is `kmx local up`'s, in `kmx local up`'s order,
// with the same waits and the same fail-closed checks — what it ADDS is the
// one thing `local up` deliberately does not: a fixed Orka Agent and a question
// put to it. The runtime is Orka; nothing on this path installs, reads or
// depends on the retired runtime.
//
// It is deterministic and non-interactive on purpose. There is no model
// picker and no prompt: the same command on the same machine produces the
// same cluster, the same Provider and the same Agent, which is what lets an
// unattended caller rerun it and compare. Choosing your own model and
// authoring your own agent is `kmx quickstart --interactive`.
//
// This command does not enable governance; existing governance may survive
// a rerun and is not assessed here.
func (a *App) Quickstart(opt QuickstartOptions) error {
	started := a.timeNow()
	a.quickstartPrepared, a.quickstartPlacement = nil, nil
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
	// exactly one document.
	if asJSON {
		a.Run.Stdout = a.Err
	}
	if err := a.validateKindTarget(); err != nil {
		return err
	}

	// Equip the machine first. Everything after this point assumes kind and
	// kubectl and Helm are runnable, and the whole point of the command is
	// that a machine missing them still gets there.
	if err := a.preflight(depKind, depKubectl, depHelm, a.engineDependency()); err != nil {
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
		if a.quickstartPlacement == nil {
			return fmt.Errorf("quickstart has no successful suite placement; no answer Task sent")
		}
		answer, err = a.answerSuitePlacement(a.operationContext(), *a.quickstartPlacement, task)
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
		"Existing governance is not assessed by quickstart.",
		a.presenter().Warning("NOTE"))
	a.quickstartNext(cliui.New(a.Err), result)
	return nil
}

// quickstartSteps is the supported clean-machine sequence, in order.
func (a *App) quickstartSteps() []quickstartStep {
	return []quickstartStep{
		{"Prepare kind cluster", a.stepCluster},
		{"Deploy Ollama", a.stepOllama},
		{"Pull model " + a.Cfg.Model, a.stepModel},
		{"Install Orka " + OrkaVersion, a.stepQuickstartOrka},
		{"Deploy the " + QuickstartAgent + " agent", a.stepQuickstartSuite},
	}
}

func (a *App) quickstartResult(question, answer string, started time.Time) QuickstartResult {
	result := QuickstartResult{
		OK:      true,
		Context: a.Cfg.KubeContext,
		Cluster: a.Cfg.KindCluster,
		Agent:   QuickstartAgent,
		Manifest: fmt.Sprintf("Orka Provider/Agent %s in %s, generated by kmx (authoring: docs/kmx.md#kmx-create)",
			QuickstartAgent, OrkaNamespace),
		Question:       question,
		Answer:         answer,
		Governed:       false,
		Next:           a.quickstartFollowups(),
		ElapsedSeconds: a.timeNow().Sub(started).Seconds(),
	}
	if a.quickstartPrepared != nil && a.quickstartPlacement != nil {
		result.SuiteDigest = a.quickstartPlacement.Summary.SuiteDigest
		result.Workspace, result.Layout, result.Evidence = a.quickstartPrepared.Workspace, a.quickstartPrepared.Layout, a.quickstartPrepared.Evidence
		result.Manifest = fmt.Sprintf("AgentSuite %s, member %s in %s", result.SuiteDigest, a.quickstartPlacement.Summary.Member, a.quickstartPlacement.Summary.Target.Namespace)
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
		ResultPort:           a.quickstartResultPort,
	}
}

// quickstartFollowups are the commands this cluster can actually run next.
func (a *App) quickstartFollowups() []string {
	return []string{
		a.operationCommand("chat", QuickstartAgent, "--runtime", "orka", "--namespace", OrkaNamespace, "ask it something else"),
		a.operationCommand("create", "--help"),
		a.operationCommand("orka", "status"),
	}
}

func (a *App) quickstartNext(ui cliui.Output, result QuickstartResult) {
	down := a.operationCommand("local", "down")
	if ui.Rich() {
		a.notef("\n%s", ui.Actions("Next", []cliui.Action{
			{Label: "Ask another question", Command: result.Next[0]},
			{Label: "Author your own suite workspace", Command: result.Next[1], Detail: "docs/kmx.md#kmx-create"},
			{Label: "Inspect the Orka runtime", Command: result.Next[2], Detail: "what is installed, and what it can resolve"},
			{Label: "Delete this cluster", Command: down, Detail: "delete the cluster and everything in it"},
		}))
	} else {
		a.notef("\nNEXT  %s  # ask it something else\n"+
			"      %s  # offline authoring help; docs/kmx.md#kmx-create\n"+
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
	if len(args) > 0 && (args[0] == "local" || args[0] == "up" || args[0] == "plane" || args[0] == "down") {
		parts = append([]string{"KIND_CLUSTER=" + shellArg(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + shellArg(a.Cfg.ContainerEngine)}, parts...)
	}
	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}
	return strings.Join(parts, " ")
}
