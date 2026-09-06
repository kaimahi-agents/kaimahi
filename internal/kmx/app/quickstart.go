package app

import (
	"encoding/json"
	"fmt"
	"strings"

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
	OK             bool             `json:"ok"`
	Context        string           `json:"context"`
	Cluster        string           `json:"cluster"`
	Agent          string           `json:"agent"`
	Manifest       string           `json:"manifest"`
	Question       string           `json:"question"`
	Answer         string           `json:"answer"`
	Governed       bool             `json:"governed"`
	Tools          []QuickstartTool `json:"tools"`
	ElapsedSeconds float64          `json:"elapsed_seconds"`
	Next           []string         `json:"next"`
}

// quickstartValues turns off everything a first question cannot reach.
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
// is left is the shortest thing that can honestly be called a working agent,
// and the honest thing to say afterwards is that nothing about it is
// governed yet (governance is what you turn on next, not a gate you
// pass through first).
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

	if err := a.Guard("create a local cluster and a first agent", "kmx quickstart"); err != nil {
		return err
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Prepare kind cluster", a.stepCluster},
		{"Deploy Ollama", a.stepOllama},
		{"Pull model " + a.Cfg.Model, a.stepModel},
		{"Install kagent (first-answer profile)", func() error { return a.installKagent(quickstartValues...) }},
		{"Deploy the " + agent + " agent", a.stepAgent},
	}
	total := len(steps) + 1
	for i, step := range steps {
		if err := a.runPhase(phase{current: i + 1, total: total, name: step.name}, step.fn); err != nil {
			return err
		}
	}

	var raw string
	var status int
	if err := a.runPhase(phase{current: total, total: total, name: "Ask " + agent + " a question"}, func() error {
		var err error
		raw, status, err = a.askAgent(agent, task, "", false)
		return err
	}); err != nil {
		return err
	}
	if status != 0 {
		fmt.Fprint(a.Err, raw)
		return fmt.Errorf("the agent was deployed but did not answer: kagent invoke exited %d", status)
	}
	answer := strings.TrimSpace(firstText(parseTask(raw)))
	if answer == "" {
		// The agent replied with something this build does not recognise.
		// Print what kagent printed rather than claim an answer we cannot
		// see: "it answered" is the one thing this command asserts.
		fmt.Fprint(a.Err, raw)
		return fmt.Errorf("the agent was deployed but no reply could be read from its response")
	}

	result := QuickstartResult{
		OK:       true,
		Context:  a.Cfg.KubeContext,
		Cluster:  a.Cfg.KindCluster,
		Agent:    agent,
		Manifest: "k8s/hello-world.yaml (embedded in kmx; `kmx agent create` writes your own)",
		Question: task,
		Answer:   answer,
		Governed: false,
		Next: []string{
			"kmx agent chat " + agent + " \"ask it something else\"",
			"kmx agent create <name> --description '...'",
			"kmx up",
			"kmx plane",
			"kmx govern " + a.Cfg.Credential,
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

	fmt.Fprintf(a.Out, "\n%s\n", answer)
	a.complete("An agent answered", started)
	// The ungoverned state is stated, in the same words `kmx up` uses, and
	// it is stated as a fact about this cluster rather than as a warning
	// nobody reads. Governance is the next command, not a missing step.
	a.notef("\nUNGOVERNED  The Kaimahi governance plane is NOT deployed.\n"+
		"Nothing that agent does is metered, budgeted, approved or audited.\n"+
		"  kmx plane          # the metering proxy and its ledger\n"+
		"  kmx govern %s  # put %s behind it (docs/spend.md)",
		a.Cfg.Credential, agent)
	a.notef("\nNEXT  kmx agent chat %s \"...\"   ask it something else\n"+
		"      kmx agent create <name>       your own agent, as reviewable YAML\n"+
		"      kmx up                        the rest of the runtime (tool server, second agent)\n"+
		"      kmx down                      delete the cluster and everything in it", agent)
	return nil
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
