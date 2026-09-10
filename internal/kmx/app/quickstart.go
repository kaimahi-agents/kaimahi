package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/toolchain"
)

// QuickstartOptions configure the shortest path to a first answer.
type QuickstartOptions struct {
	// Output is "text" for a person or "json" for whatever is driving.
	Output string
	Agent  string
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

// quickstartValues turns off everything a first question cannot reach, only
// when creating a release. Existing releases are never reduced to this profile.
//
// Measured on the images the chart pulls at kagent 0.9.12 (linux/amd64,
// compressed): the console is 115MB, the bundled tool server 215MB and the
// MCP controller 32MB — 362MB and two more pods to become Ready before
// anybody sees an answer, for three components the hello-world agent never
// touches. `kmx up` afterwards installs the full set by simply not passing
// these, and helm reconciles the difference.
//
// The console is turned off by replica count rather than a switch because
// the chart has no `ui.enabled` at this version. That is a fact about kagent
// 0.9.12, and if a later chart grows the switch this should use it.
var quickstartValues = []string{
	"--set-string", "kaimahi.profile=first-answer",
	"--set", "kagent-tools.enabled=false",
	"--set", "kmcp.enabled=false",
	"--set", "ui.replicas=0",
}

// Quickstart is the whole distance from a machine with a container engine to
// an agent answering a question, in one command.
//
// It is not a new journey. Every step is `kmx up`'s, in `kmx up`'s order,
// with the same waits and the same fail-closed checks — what it does is
// DEFER: the tool server, the second agent and the governance plane are not
// on the path to a first answer, so they are not on this path either. What
// is left is the shortest thing that can honestly be called a working agent.
// This command does not enable governance; existing governance may survive
// a rerun and is not assessed here.
func (a *App) Quickstart(opt QuickstartOptions) error {
	started := a.timeNow()
	agent, task := opt.Agent, opt.Task
	if agent == "" {
		agent = config.DefaultAgent
	}
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
	// (cmd/kmx wires Runner.Stdout to it), so `kind create`, `helm upgrade`
	// and every `kubectl wait` would land in front of the JSON and the
	// caller would get "Expecting value: line 1 column 1". Under --output
	// json those go to stderr with everything else humans read, leaving
	// stdout carrying exactly one document. `kmx metrics` already draws this
	// line for the same reason.
	if asJSON {
		a.Run.Stdout = a.Err
	}
	if err := a.validateKindTarget(); err != nil {
		return err
	}

	// Equip the machine first. Everything after this point assumes kind,
	// kubectl and Helm are runnable, and the whole point of the command is
	// that a machine which had none of them still gets there.
	if err := a.preflight(depKind, depKubectl, depHelm, a.engineDependency()); err != nil {
		return err
	}
	if len(a.provisioned) > 0 && !asJSON {
		a.notef("Tools this run is using:")
		toolchain.Report(a.Err, a.provisioned)
	}

	if err := a.GuardCreate("create a local cluster and a first agent", "kmx quickstart"); err != nil {
		return err
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Prepare kind cluster", a.stepCluster},
		{"Deploy Ollama", a.stepOllama},
		{"Pull model " + a.Cfg.Model, a.stepModel},
		{"Install or verify kagent", a.stepQuickstartKagent},
		{"Deploy the " + agent + " agent", a.stepAgent},
	}
	total := len(steps) + 1
	for i, step := range steps {
		if err := a.runPhase(phase{current: i + 1, total: total, name: step.name}, step.fn); err != nil {
			return err
		}
	}

	var answer string
	if err := a.runPhase(phase{current: total, total: total, name: "Ask " + agent + " a question"}, func() error {
		raw, status, err := a.askAgent(agent, task, "", false, ChatRetryable)
		if err != nil {
			return err
		}
		answer, err = quickstartAnswer(raw, status)
		if err != nil {
			fmt.Fprint(a.Err, safeTerminal(raw))
		}
		return err
	}); err != nil {
		return err
	}

	result := QuickstartResult{
		OK:       true,
		Context:  a.Cfg.KubeContext,
		Cluster:  a.Cfg.KindCluster,
		Agent:    agent,
		Manifest: "k8s/hello-world.yaml (embedded kagent example; Orka authoring: docs/kmx.md#kmx-agent-create)",
		Question: task,
		Answer:   answer,
		Governed: false,
		Next: []string{
			a.operationCommand("agent", "chat", agent, "ask it something else"),
			a.operationCommand("orka", "install"),
			a.operationCommand("up"),
			a.operationCommand("plane"),
			a.operationCommand("govern", a.Cfg.Credential),
		},
		ElapsedSeconds: a.timeNow().Sub(started).Seconds(),
	}
	for _, t := range a.provisioned {
		result.Tools = append(result.Tools, QuickstartTool{Name: t.Name, Version: t.Version, Source: string(t.Source)})
	}

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
		"  %s  # configure agent routing (docs/spend.md)",
		a.presenter().Warning("GOVERNANCE"), result.Next[3], result.Next[4])
	a.quickstartNext(cliui.New(a.Err), result)
	return nil
}

func (a *App) quickstartNext(ui cliui.Output, result QuickstartResult) {
	down := a.operationCommand("down")
	if ui.Rich() {
		a.notef("\n%s", ui.Actions("Next", []cliui.Action{
			{Label: "Ask another question", Command: result.Next[0]},
			{Label: "Install Orka", Command: result.Next[1], Detail: "prerequisite for Orka authoring; docs/kmx.md#kmx-agent-create"},
			{Label: "Install the full runtime", Command: result.Next[2], Detail: "tool server and second agent"},
			{Label: "Delete this cluster", Command: down, Detail: "delete the cluster and everything in it"},
		}))
	} else {
		a.notef("\nNEXT  %s  # ask it something else\n"+
			"      %s  # prerequisite for Orka authoring; docs/kmx.md#kmx-agent-create\n"+
			"      %s  # the rest of the runtime (tool server, second agent)\n"+
			"      %s  # delete the cluster and everything in it", result.Next[0], result.Next[1], result.Next[2], down)
	}
}

// quickstartKagent keeps the former internal call site on the canonical
// monotonic implementation; there is intentionally no second Helm flow.
func (a *App) quickstartKagent() error { return a.stepQuickstartKagent() }

func quickstartAnswer(raw string, status int) (string, error) {
	if status != 0 {
		return "", fmt.Errorf("the agent was deployed but did not answer: kagent invoke exited %d", status)
	}
	task := parseTask(raw)
	if task.Status.State != "completed" {
		return "", fmt.Errorf("the agent was deployed but its task did not complete (state %q)", task.Status.State)
	}
	answer := strings.TrimSpace(firstText(task))
	if strings.TrimSpace(safeTerminal(answer)) == "" {
		return "", fmt.Errorf("the agent was deployed but no reply could be read from its response")
	}
	return answer, nil
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

// parseTask decodes the A2A task out of kagent's combined output, returning
// a zero task when there is nothing to decode.
func parseTask(combined string) a2aTask {
	var task a2aTask
	line := lastJSONLine(combined)
	if line == "" {
		return task
	}
	if err := json.Unmarshal([]byte(line), &task); err != nil {
		return a2aTask{}
	}
	return task
}
