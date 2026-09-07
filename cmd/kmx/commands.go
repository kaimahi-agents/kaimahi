package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seam"
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
// different promises. `up` brings up the RUNTIME — every agent, the tool
// server, everything a later step might need. `quickstart` promises one
// thing, an answer, and defers everything that is not on the way to it.
// Folding them together would mean one command with two contracts and a flag
// deciding which you got.
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
	cmd := &cobra.Command{Use: "plane", Short: "Deploy the Kaimahi governance plane", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&opt.Step, "step", "", "run one step only: "+strings.Join(app.PlaneSteps, ", "))
	cmd.Flags().StringVar(&opt.Source, "source", "", "build from this checkout ('-' forces module fetch)")
	_ = cmd.RegisterFlagCompletionFunc("step", staticCompletion(app.PlaneSteps))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Plane(opt) })
	return cmd
}

func newGovernCommand(state *commandState) *cobra.Command {
	var opt app.GovernOptions
	var ttl string
	cmd := &cobra.Command{Use: "govern [credential]", Short: "Issue a credential and govern an agent", Args: usageArgs(0, 1, "kmx govern [<credential>] [flags]")}
	cmd.Flags().StringVar(&opt.Agent, "agent", config.DefaultAgent, "agent to put behind the plane")
	cmd.Flags().StringVar(&opt.Preset, "preset", config.GovernedModelConfig, "governed ModelConfig")
	cmd.Flags().StringVar(&opt.Secret, "secret", config.GovernedSecret, "agent-side Secret")
	cmd.Flags().StringVar(&opt.SecretNamespace, "secret-namespace", config.DefaultNamespace, "Secret namespace")
	cmd.Flags().StringVar(&ttl, "ttl", "-", "credential lifetime, e.g. 30d (default: plane policy)")
	_ = cmd.RegisterFlagCompletionFunc("agent", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeLiveAgents(cmd, nil, toComplete)
	})
	cmd.RunE = appRun(state, func(a *app.App) error {
		var err error
		if opt.TTLSeconds, err = admin.ParseTTL(ttl); err != nil {
			return err
		}
		return a.Govern(parseOptionalCredential(cmd.Flags().Args(), a.Cfg.Credential), opt)
	})
	return cmd
}

func newCredentialsCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "credentials", Short: "List governed credentials and expiry", Args: cobra.NoArgs, RunE: appRun(state, func(a *app.App) error { return a.Credentials() })}
}

func newCredentialCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "credential", Short: "Manage credential lifecycle", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
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
	group.AddCommand(renew, newCaptureCommand(state))
	return group
}

// newCaptureCommand is the one command in kmx that accepts credential
// material, and it takes it from a terminal or not at all.
//
// One command with a SUBJECT, rather than one command per upstream: three
// upstreams take a credential today, they differ only in which token they
// want and what can be proven about it, and both of those are a row in
// internal/kmx/seam's table. A fourth upstream should be a row too, not a
// fourth command shape to learn.
func newCaptureCommand(state *commandState) *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "capture <upstream> <repository|organization>",
		Short: "Store an upstream credential in plane custody, read from the terminal",
		Long: "Store an upstream credential in plane custody, read from the terminal.\n\n" +
			"The value is typed at a prompt with the echo off. There is deliberately no\n" +
			"flag, environment variable or file that takes it instead, and a piped or\n" +
			"redirected stdin is refused rather than read: a credential that can arrive\n" +
			"through a pipe can arrive from a shell history or a CI log. It is checked\n" +
			"against the upstream before anything is stored, and it goes into a Kubernetes\n" +
			"Secret the gateway reads — never into a manifest, a file or a command line.\n\n" +
			"Upstreams:\n" + captureUpstreams(),
		Args:      usageArgs(2, 2, "kmx credential capture <upstream> <repository|organization> [--replace]"),
		ValidArgs: seam.Names(),
	}
	cmd.Flags().BoolVar(&replace, "replace", false,
		"overwrite a credential this upstream already has (it is refused otherwise)")
	cmd.RunE = appRun(state, func(a *app.App) error {
		return a.CaptureCredential(app.CaptureOptions{
			Seam:    cmd.Flags().Arg(0),
			Subject: cmd.Flags().Arg(1),
			Replace: replace,
		})
	})
	return cmd
}

func captureUpstreams() string {
	var b strings.Builder
	for _, s := range seam.All() {
		fmt.Fprintf(&b, "  kmx credential capture %s %s\n      %s; stored as Secret %s\n",
			s.Name, s.SubjectHint, s.Summary, s.Secret)
	}
	return b.String()
}

func newLedgerCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "ledger [credential]", Short: "Show spend ledger", Args: usageArgs(0, 1, "kmx ledger [<credential>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Ledger(parseOptionalCredential(cmd.Flags().Args(), a.Cfg.Credential)) })
	return cmd
}

func newGrantsCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "grants [credential]", Short: "List grants and liveness", Args: usageArgs(0, 1, "kmx grants [<credential>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Grants(parseOptionalCredential(cmd.Flags().Args(), "")) })
	return cmd
}

// newFlowCommand merges the four audit trails into one chronological reading.
//
// It defaults to ALL credentials, like grants and unlike the ledger: the
// question a flow answers is "what has been going on", and an operator who
// does not yet know which credential misbehaved cannot be asked to name it
// first. Every row is attributed, so a merged reading stays readable.
func newFlowCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "flow [credential]", Short: "Merge the four audit trails into one timeline", Args: usageArgs(0, 1, "kmx flow [<credential>]")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Flow(parseOptionalCredential(cmd.Flags().Args(), "")) })
	return cmd
}

func newAuditCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "audit <tool|approval> [credential]", Short: "Show enforcement audit trails", Args: usageArgs(1, 2, "kmx audit tool|approval [<credential>]")}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return filterCompletions([]string{"tool", "approval"}, toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		return a.Audit(cmd.Flags().Arg(0), parseOptionalCredential(cmd.Flags().Args()[1:], ""))
	})
	return cmd
}

func newUseCommand(state *commandState) *cobra.Command {
	var agent string
	cmd := &cobra.Command{Use: "use <preset>", Short: "Switch an agent to an embedded model preset", Args: usageArgs(1, 1, "kmx use <preset> [--agent <name>]")}
	cmd.Flags().StringVar(&agent, "agent", config.DefaultAgent, "agent to switch")
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Use(cmd.Flags().Arg(0), app.UseOptions{Agent: agent}) })
	cmd.ValidArgsFunction = completePresets
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

func newApprovalsCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "approvals", Short: "List pending approval requests", Args: cobra.NoArgs, RunE: appRun(state, func(a *app.App) error { return a.Approvals() })}
}

func newApproveCommand(state *commandState) *cobra.Command {
	var ttl, uses, amount string
	cmd := &cobra.Command{Use: "approve <id>", Short: "Approve a request with bounded authority", Args: usageArgs(1, 1, "kmx approve <id> [--ttl 10m] [--uses 1] [--amount n]")}
	cmd.Flags().StringVar(&ttl, "ttl", "-", "expiry")
	cmd.Flags().StringVar(&uses, "uses", "-", "maximum uses")
	cmd.Flags().StringVar(&amount, "amount", "-", "tokens or cents")
	cmd.RunE = appRun(state, func(a *app.App) error {
		t, u, m, err := parseApprovalValues(ttl, uses, amount)
		if err != nil {
			return err
		}
		return a.Approve(cmd.Flags().Arg(0), t, u, m)
	})
	return cmd
}

func newDenyCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{Use: "deny <id>", Short: "Deny a pending request", Args: usageArgs(1, 1, "kmx deny <id>")}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.Deny(cmd.Flags().Arg(0)) })
	return cmd
}

func newRequestCommand(state *commandState) *cobra.Command {
	var credential, argsJSON string
	cmd := &cobra.Command{Use: "request <tool|budget|inbound> <subject>", Short: "File an approval request", Args: usageArgs(2, 2, "kmx request <tool|budget|inbound> <subject> [--credential <name>] [--args <json>]")}
	cmd.Flags().StringVar(&credential, "credential", "", "credential the request is filed against")
	cmd.Flags().StringVar(&argsJSON, "args", "", "tool call arguments as one JSON object")
	cmd.RunE = appRun(state, func(a *app.App) error {
		kind := cmd.Flags().Arg(0)
		name := requestCredential(credential, kind, a.Cfg.Credential, a.Cfg.ToolsCredential)
		parsed, err := parseJSONArgs(argsJSON)
		if err != nil {
			return err
		}
		return a.Request(name, kind, cmd.Flags().Arg(1), parsed)
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

func newStatusCommand(state *commandState) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "status", Short: "Show grouped runtime health", Args: cobra.NoArgs}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json|yaml")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json", "yaml"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.StatusWithOptions(app.StatusOptions{Output: output}) })
	return cmd
}

func newDownCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "down", Short: "Delete the local kind cluster", Args: cobra.NoArgs, RunE: appRun(state, func(a *app.App) error { return a.Down() })}
}
