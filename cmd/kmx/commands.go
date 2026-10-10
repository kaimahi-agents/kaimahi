package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func newCtxCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "ctx [context]", Short: "Show or select the Kubernetes context", Args: usageArgs(0, 1, "kmx ctx [<context>]")}
	cmd.RunE = appRun(state, func(a *app.App) error {
		value := ""
		if len(cmd.Flags().Args()) == 1 {
			value = cmd.Flags().Arg(0)
		}
		return a.Ctx(value)
	})
	cmd.ValidArgsFunction = completeContexts
	return cmd
}

func newContextCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "context", Short: "Show or select KMX's Kubernetes access context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	show := &cobra.Command{Use: "show", Short: "Show the effective context, selection source and posture", Args: cobra.NoArgs,
		RunE: appRun(state, func(a *app.App) error { return a.Ctx("") })}
	use := &cobra.Command{Use: "use <context>", Short: "Remember KMX's context without changing kubectl current-context",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("context use requires a non-empty context name")
			}
			return nil
		}, ValidArgsFunction: completeContexts}
	use.RunE = appRun(state, func(a *app.App) error { return a.Ctx(use.Flags().Arg(0)) })
	group.AddCommand(show, use)
	return group
}

func newLocalCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "local", Short: "Provision or delete the local kind environment", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newUpCommand(state), newDownCommand(state))
	return group
}

// newQuickstartCommand is the front door: one command, from a machine with a
// container engine to an agent that has answered a question.
//
// It is separate from `local up` rather than a flag on it because the two
// make different promises. `local up` brings up the RUNTIME — a cluster, a
// keyless model server and the pinned Orka release — and deploys no agent.
// `quickstart` promises one thing, an answer, and adds what `local up` deliberately
// leaves out: a fixed Orka Agent and a question put to it. Folding them
// together would mean one command with two contracts and a flag deciding
// which you got.
//
// --interactive is a mode of the same promise, not a second contract: a
// person steers the agent and model choices instead of taking the fixed
// ones. Each mode owns its flags, and a flag from the other mode is refused
// before configuration loads so it can never be silently ignored.
func newQuickstartCommand(state *commandState) *cobra.Command {
	var opt app.QuickstartOptions
	var wizard app.QuickstartWizardOptions
	var interactive bool
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "From nothing to an agent answering a question",
		Long: "From nothing to an agent answering a question.\n\n" +
			"Without --interactive, deploys the fixed hello-world agent, asks it one question and prints the answer; repeatable and safe for automation.\n" +
			"With --interactive, opens a guided terminal UI to author your own Orka agent and choose its model while the local runtime starts.",
	}
	flags := cmd.Flags()
	flags.BoolVarP(&interactive, "interactive", "i", false, "author your own Orka agent in a guided terminal UI")
	flags.StringVarP(&opt.Output, "output", "o", "text", "output: text|json (not with --interactive)")
	// No --agent flag: quickstart deploys the hello-world manifest kmx
	// carries, so a flag naming a different agent would deploy one thing and
	// question another. Your own agent is --interactive or `kmx agent create`,
	// and asking any agent anything is `kmx agent chat`.
	flags.StringVar(&opt.Task, "task", config.DefaultTask, "the question to ask; with --interactive, an optional first Orka Task prompt that ignores this default")
	flags.StringVar(&wizard.Create.Instructions, "instructions", "", "file containing the system message (--interactive only)")
	flags.StringVar(&wizard.Create.Tools, "tools", "", "comma-separated Orka tool names (--interactive only; default: k8s-get-resources)")
	flags.StringVar(&wizard.Create.Skills, "skills", "", "comma-separated explicit Orka skill names (--interactive only)")
	flags.StringVar(&wizard.Create.ResultServiceAccount, "result-service-account", "", "existing ServiceAccount for Task result access (--interactive only)")
	flags.StringVar(&wizard.Create.Out, "out", "", "manifest output path (--interactive only)")
	flags.BoolVar(&wizard.Verbose, "verbose", false, "show chat WORKING and TIMING details (--interactive only)")
	flags.StringVar(&wizard.AzureDiscovery, "azure-discovery", "cli", "AKS cluster listing: cli or sdk (DefaultAzureCredential) (--interactive only)")
	flags.StringVar(&wizard.Inference, "inference", "auto", "legacy discovery hint; the guided setup always requires an explicit source/model selection (--interactive only)")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	_ = cmd.RegisterFlagCompletionFunc("azure-discovery", staticCompletion([]string{"cli", "sdk"}))
	_ = cmd.RegisterFlagCompletionFunc("inference", staticCompletion([]string{"auto", "copilot", "local", "foundry"}))
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		return quickstartModeFlags(cmd, interactive)
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		if !interactive {
			return a.Quickstart(opt)
		}
		// The fixed question is the non-interactive default; the guided
		// setup runs a first Task only when one was asked for.
		if cmd.Flags().Changed("task") {
			wizard.Create.Task = opt.Task
		}
		return a.QuickstartWizard(wizard)
	})
	return cmd
}

// quickstartInteractiveOnly are the flags that shape the guided setup. They
// mean nothing to the fixed hello-world run.
var quickstartInteractiveOnly = []string{"instructions", "tools", "skills", "result-service-account", "out", "verbose", "azure-discovery", "inference"}

// quickstartModeFlags refuses a flag that belongs to the other quickstart
// mode. It runs as argument validation, before any configuration is loaded.
func quickstartModeFlags(cmd *cobra.Command, interactive bool) error {
	flags := cmd.Flags()
	if interactive {
		if flags.Changed("output") {
			return fmt.Errorf("--output does not apply to kmx quickstart --interactive: the guided setup has no machine-readable output; use kmx quickstart -o json for automation")
		}
		return nil
	}
	for _, name := range quickstartInteractiveOnly {
		if flags.Changed(name) {
			return fmt.Errorf("--%s requires --interactive: use kmx quickstart --interactive --%s ...", name, name)
		}
	}
	return nil
}

func newUpCommand(state *commandState) *cobra.Command {
	var step string
	cmd := &cobra.Command{Use: "up", Short: "Bring up the local runtime", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&step, "step", "", "run one step only: "+strings.Join(app.UpSteps, ", "))
	_ = cmd.RegisterFlagCompletionFunc("step", staticCompletion(app.UpSteps))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Up(step) })
	return cmd
}

// `kmx status` is the runtime report. Its -o flag survives with one value,
// and json/yaml are refused BY NAME rather than dropped: a script pinned to
// `-o json` has to be told the document is gone, and a flag that silently
// ignores what it was given is worse than one that says no.
func newStatusCommand(state *commandState) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "status", Short: "Show the runtime Orka has installed, and what it can resolve", Args: cobra.NoArgs}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.StatusWithOptions(app.StatusOptions{Output: output}) })
	return cmd
}

func newDownCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "down", Short: "Delete the local kind cluster", Args: cobra.NoArgs, RunE: appRun(state, func(a *app.App) error { return a.Down() })}
}
