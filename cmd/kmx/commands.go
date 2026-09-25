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
func newQuickstartCommand(state *commandState) *cobra.Command {
	var opt app.QuickstartOptions
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "From nothing to an agent answering a question",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "text", "output: text|json")
	// No --agent flag: quickstart deploys the hello-world manifest kmx
	// carries, so a flag naming a different agent would deploy one thing and
	// question another. Your own agent is `kmx agent create`, and asking any
	// agent anything is `kmx agent chat`.
	cmd.Flags().StringVar(&opt.Task, "task", config.DefaultTask, "the question to ask it")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Quickstart(opt) })
	return cmd
}

func newQuickstartWizardCommand(state *commandState) *cobra.Command {
	var opt app.QuickstartWizardOptions
	cmd := &cobra.Command{
		Use:   "quickstart-wizard",
		Short: "Create your first Orka agent while its local runtime starts",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&opt.Create.Instructions, "instructions", "", "file containing the system message")
	cmd.Flags().StringVar(&opt.Create.Tools, "tools", "", "comma-separated Orka tool names (default: k8s-get-resources)")
	cmd.Flags().StringVar(&opt.Create.Skills, "skills", "", "comma-separated explicit Orka skill names")
	cmd.Flags().StringVar(&opt.Create.Task, "task", "", "optional first Orka Task prompt")
	cmd.Flags().StringVar(&opt.Create.ResultServiceAccount, "result-service-account", "", "existing ServiceAccount for Task result access")
	cmd.Flags().StringVar(&opt.Create.Out, "out", "", "manifest output path")
	cmd.Flags().BoolVar(&opt.Verbose, "verbose", false, "show chat WORKING and TIMING details")
	cmd.Flags().StringVar(&opt.AzureDiscovery, "azure-discovery", "cli", "AKS cluster listing: cli or sdk (DefaultAzureCredential)")
	_ = cmd.RegisterFlagCompletionFunc("azure-discovery", staticCompletion([]string{"cli", "sdk"}))
	cmd.Flags().StringVar(&opt.Inference, "inference", "auto", "legacy discovery hint; wizard always requires an explicit source/model selection")
	_ = cmd.RegisterFlagCompletionFunc("inference", staticCompletion([]string{"auto", "copilot", "local", "foundry"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.QuickstartWizard(opt) })
	return cmd
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
	issue := &cobra.Command{Use: "issue <name>", Short: "Issue a credential to a Secret or discard its bearer", Args: usageArgs(1, 1, "kmx credential issue <name> (--discard | --secret <name>) [--namespace <namespace>] [--ttl duration]")}
	issue.Flags().BoolVar(&discard, "discard", false, "discard the one-time bearer instead of storing or printing it")
	issue.Flags().StringVar(&secret, "secret", "", "store the one-time bearer in this Kubernetes Secret")
	issue.Flags().StringVar(&namespace, "namespace", config.DefaultNamespace, "Secret namespace")
	issue.Flags().StringVar(&issueTTL, "ttl", "-", "credential lifetime, e.g. 30d (default: plane policy)")
	issue.MarkFlagsOneRequired("discard", "secret")
	issue.MarkFlagsMutuallyExclusive("discard", "secret")
	issue.RunE = func(cmd *cobra.Command, _ []string) error {
		if !discard && secret == "" {
			return fmt.Errorf("kmx credential issue requires a non-empty --secret <name>")
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
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Ledger(parseOptionalCredential(cmd.Flags().Args(), a.Cfg.Credential)) })
	return cmd
}

// newFlowCommand reads the model ledger chronologically.
//
// It defaults to ALL credentials, unlike the ledger: the
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
