package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
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

// newQuickstartCommand is the front door: one command, from a machine with a
// container engine to an agent that has answered a question.
//
// It is a sibling of `up` rather than a flag on it because the two make
// different promises. `up` brings up the RUNTIME — a cluster, a keyless model
// server and the pinned Orka release — and deploys no agent. `quickstart`
// promises one thing, an answer, and adds the one thing `up` deliberately
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

func newPlaneCommand(state *commandState) *cobra.Command {
	var opt app.PlaneOptions
	cmd := &cobra.Command{Use: "plane", Short: "Deploy the Kaimahi model-traffic bridge", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&opt.Step, "step", "", "run one step only: "+strings.Join(app.PlaneSteps, ", "))
	cmd.Flags().StringVar(&opt.Source, "source", "", "build from this checkout ('-' forces module fetch)")
	_ = cmd.RegisterFlagCompletionFunc("step", staticCompletion(app.PlaneSteps))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Plane(opt) })
	return cmd
}

func newCredentialsCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "credentials", Short: "List governed credentials and expiry", Args: cobra.NoArgs, RunE: appRun(state, func(a *app.App) error { return a.Credentials() })}
}

func newCredentialCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "credential", Short: "Manage credential lifecycle", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	var discard bool
	var secret string
	var namespace string
	var issueTTL string
	issue := &cobra.Command{Use: "issue <name>", Short: "Issue a credential to a Secret or discard its bearer", Args: usageArgs(1, 1, "kmx credential issue <name> (--discard | --secret <name> --namespace <namespace>) [--ttl duration]")}
	issue.Flags().BoolVar(&discard, "discard", false, "discard the one-time bearer instead of storing or printing it")
	issue.Flags().StringVar(&secret, "secret", "", "store the one-time bearer in this Kubernetes Secret")
	// No default: a one-time token must be written into the operator's chosen
	// namespace, not silently issued into the wrong place.
	issue.Flags().StringVar(&namespace, "namespace", "", "namespace the Secret is created in (required with --secret)")
	issue.Flags().StringVar(&issueTTL, "ttl", "-", "credential lifetime, e.g. 30d (default: plane policy)")
	issue.MarkFlagsOneRequired("discard", "secret")
	issue.MarkFlagsMutuallyExclusive("discard", "secret")
	issue.RunE = func(cmd *cobra.Command, _ []string) error {
		if !discard && secret == "" {
			return fmt.Errorf("kmx credential issue requires a non-empty --secret <name>")
		}
		if !discard && strings.TrimSpace(namespace) == "" {
			return fmt.Errorf("kmx credential issue --secret %s also requires --namespace <namespace>:\n"+
				"  the token is shown exactly once, so the namespace its Secret lands in is named rather than guessed", secret)
		}
		name := issue.Flags().Arg(0)
		if err := admin.ValidCredentialName(name); err != nil {
			return err
		}
		parsed, err := admin.ParseTTL(issueTTL)
		if err != nil {
			return err
		}
		a, err := state.operationApplication(cmd)
		if err != nil {
			return err
		}
		if discard {
			return a.IssueIdentityCredential(name, parsed)
		}
		return a.IssueCredentialToSecret(name, secret, namespace, parsed)
	}
	var ttl string
	renew := &cobra.Command{Use: "renew <name>", Short: "Extend credential expiry", Args: usageArgs(1, 1, "kmx credential renew <name> [--ttl 720h]")}
	renew.Flags().StringVar(&ttl, "ttl", "-", "new lifetime from now")
	renew.RunE = appRun(state, func(a *app.App) error {
		if err := admin.ValidCredentialName(renew.Flags().Arg(0)); err != nil {
			return err
		}
		parsed, err := admin.ParseTTL(ttl)
		if err != nil {
			return err
		}
		return a.RenewCredential(renew.Flags().Arg(0), parsed)
	})
	group.AddCommand(issue, renew)
	return group
}

func newLedgerCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "ledger [credential]", Short: "Show spend ledger", Args: usageArgs(0, 1, "kmx ledger [<credential>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Ledger(parseOptionalCredential(cmd.Flags().Args(), "")) })
	return cmd
}

// newFlowCommand reads the model ledger chronologically.
//
// Like the ledger, it defaults to ALL credentials: the
// question a flow answers is "what has been going on", and an operator who
// does not yet know which credential misbehaved cannot be asked to name it
// first. Every row is attributed, so a merged reading stays readable.
func newFlowCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "flow [credential]", Short: "Show model activity in one timeline", Args: usageArgs(0, 1, "kmx flow [<credential>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Flow(parseOptionalCredential(cmd.Flags().Args(), "")) })
	return cmd
}

func newBudgetCommand(state *commandState) *cobra.Command {
	var cents, tokens string
	cmd := &cobra.Command{Use: "budget [credential]", Short: "Replace monthly budget caps", Args: usageArgs(0, 1, "kmx budget [<credential>] [--cents n|-] [--tokens n|-]")}
	cmd.Flags().StringVar(&cents, "cents", "-", "monthly cap in cents ('-' for none)")
	cmd.Flags().StringVar(&tokens, "tokens", "-", "monthly cap in tokens ('-' for none)")
	cmd.RunE = appRun(state, func(a *app.App) error {
		c, t, err := parseBudgetValues(cents, tokens)
		if err != nil {
			return err
		}
		return a.Budget(parseOptionalCredential(cmd.Flags().Args(), a.Cfg.Credential), c, t)
	})
	return cmd
}

func newBackupCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "backup [file]", Short: "Back up the governance database", Args: usageArgs(0, 1, "kmx backup [<file>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Backup(parseOptionalCredential(cmd.Flags().Args(), "")) })
	return cmd
}

func newRestoreCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "restore <file>", Short: "Replace the governance database from a backup", Args: usageArgs(1, 1, "kmx restore <file>")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Restore(cmd.Flags().Arg(0)) })
	return cmd
}

func newMetricsCommand(state *commandState) *cobra.Command {
	var pod string
	cmd := &cobra.Command{Use: "metrics", Short: "Print one proxy replica's Prometheus metrics", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&pod, "pod", "", "proxy replica (default first Ready)")
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Metrics(pod) })
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
