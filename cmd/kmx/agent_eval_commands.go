package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func newAgentEvalCommand(state *commandState) *cobra.Command {
	var native app.SuiteWorkspaceEvaluationOptions
	group := &cobra.Command{
		Use: "eval", Short: "Evaluate a native workspace or prepare and compare saved evidence",
		Long: "Evaluate every authored case in one packaged tool-free native Orka or exact v0.10.3 Go kagent workspace, discovered from the nearest ancestor workspace.yaml or selected exactly with --workspace.\n" +
			"Orka requires --inference provider:NAME and result-access ServiceAccount/RBAC. Kagent requires author-owned --bindings FILE, binding namespace and unsecure auth; trusted-proxy and --result-port refuse. No runtime or model override, judge, subset or content output.\n" +
			"Packaged AgentSessions requires --development-host FILE: an explicit private operator observation of a pinned built-in chat host on literal loopback, unauthenticated. It refuses all cluster/inference/bindings/result selectors and loads no cluster configuration.\n" +
			"Development journals retain instructions, inputs, answers and per-exec config in cleartext under operator-managed retention. This is not authenticated remote promotion.\n" +
			"Each case submits one fresh Task or Session, never retried. Failure, unknown, drift or cancellation stops later cases without exporting a receipt; resources may remain.\n" +
			"--out must be a new file in an existing private directory outside workspace/cache trees. Only full passing metadata is exported, unsigned and not a policy gate.\n" +
			"Case inputs are sent to the runtime and model; upstream storage/audit policy may retain them. Real native CLI execution qualification remains separate from hermetic checks.",
		Example: "  kmx eval --to-context kind-demo --inference provider:inference --out ../private/passing.json\n" +
			"  kmx eval --workspace ./workspace --to-context remote --to-namespace orka-system --inference provider:inference --out ../private/passing.json",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("native evaluation accepts no positional references")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateContainerEngineFlag(cmd, args); err != nil {
				return err
			}
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return fmt.Errorf("--context cannot be used with native eval; use --to-context")
			}
			if native.DevelopmentHost != "" {
				for _, name := range []string{"to-context", "to-namespace", "inference", "bindings", "result-port", "container-engine"} {
					if cmd.Flag(name).Changed {
						return fmt.Errorf("--development-host refuses --%s", name)
					}
				}
			}
			for _, name := range []string{"workspace", "to-context", "to-namespace", "inference", "bindings", "development-host", "out", "result-port"} {
				if flag := cmd.Flag(name); flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("native eval requires non-empty --%s", name)
				}
			}
			if native.CaseTimeout <= 0 {
				return fmt.Errorf("native evaluation requires a positive --case-timeout")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			selectedNative := native
			if !cmd.Flags().Changed("to-namespace") {
				selectedNative.ToNamespace = ""
			}
			if !cmd.Flags().Changed("result-port") {
				selectedNative.ResultPort = ""
			}
			prepared, err := app.PrepareSuiteWorkspaceEvaluation(ctx, selectedNative)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, prepared.Close()) }()
			if selectedNative.DevelopmentHost != "" {
				return (&app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}).EvaluateSuiteWorkspace(ctx, prepared)
			}
			selected := *state
			selected.contextFlag = native.ToContext
			a, err := selected.operationApplication(cmd)
			if err != nil {
				return err
			}
			a.Cfg.ContextSource = config.SourceFlag
			// Native retry advice must not synthesize the refused global --context.
			parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine), "kmx"}
			for _, arg := range state.argv {
				parts = append(parts, quoteShell(arg))
			}
			a.InvocationCommand = strings.Join(parts, " ")
			return a.EvaluateSuiteWorkspace(ctx, prepared)
		},
	}
	group.Flags().StringVar(&native.Workspace, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	group.Flags().StringVar(&native.ToContext, "to-context", "", "explicit native evaluation context")
	group.Flags().StringVar(&native.ToNamespace, "to-namespace", app.OrkaNamespace, "native evaluation namespace")
	group.Flags().StringVar(&native.Inference, "inference", "", "existing inference Provider, as provider:NAME")
	group.Flags().StringVar(&native.Bindings, "bindings", "", "author-owned KagentBindings file for the packaged kagent runtime")
	_ = group.MarkFlagFilename("bindings")
	group.Flags().StringVar(&native.DevelopmentHost, "development-host", "", "private JSON observation selecting a loopback unauthenticated AgentSessions development host")
	_ = group.MarkFlagFilename("development-host", "json")
	group.Flags().StringVar(&native.Out, "out", "", "new private full-passing receipt file outside workspace/cache")
	group.Flags().DurationVar(&native.CaseTimeout, "case-timeout", 5*time.Minute, "deadline per case, at most 9m")
	group.Flags().StringVar(&native.ResultPort, "result-port", "19180", "local loopback result-forward port")
	_ = group.MarkFlagDirname("workspace")
	for _, name := range []string{"out"} {
		_ = group.MarkFlagRequired(name)
	}
	diff := &cobra.Command{
		Use:   "diff <before.json> <after.json>",
		Short: "Compare assertion outcomes in two saved evaluation receipts",
		Long: "Compare private native suite passing evidence or saved v2 evaluation receipts without cluster configuration, network access, or model calls.\n" +
			"Reports revision identities and every case/assertion change; never prints answers or assertion operands.\n" +
			"Exits nonzero for regressions, lost or missing evidence, changed sets, or incompatible runtime identities.\n" +
			"A complete stable comparison or fixes can exit zero even when both evaluations failed.\n" +
			"Legacy receipts are recognized but cannot establish assertion-level comparisons.",
		Args: usageArgs(2, 2, "kmx eval diff <before.json> <after.json>"),
		// Keep helpers cluster-independent and no-inference when eval gains an operational hook.
		PersistentPreRunE: validateContainerEngineFlag,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			return a.DiffAgentEvaluations(app.EvaluationDiffOptions{Before: args[0], After: args[1]})
		},
	}
	var opt app.SessionReferenceExtractionOptions
	from := &cobra.Command{
		Use:   "from-sessions <session-uid>...",
		Short: "Extract private single-turn session references without copying recorded content",
		Long: "Read one or more completed, fresh text-chat sessions and bind their verified journal prefixes\n" +
			"to authored assertion-only YAML (id, expectContains and/or assertions; no input).\n" +
			"Write private UID/digest references to --out; an existing manifest is atomically appended.\n" +
			"Duplicates refuse the whole operation. No inference, recorded inputs or answers are exported.\n" +
			"Case IDs combine the authored id with a stable source UID digest. References inherit source TTL.\n" +
			"Keep manifests private; endpoint selection is not data-owner authorization.",
		Args:              usageArgs(1, 1000, "kmx eval from-sessions <session-uid>... --sessions <source> --assertions <case.yaml> --out <manifest.json>"),
		PersistentPreRunE: validateContainerEngineFlag,
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.SessionUIDs = args
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Run: &run.Runner{Context: cmd.Context()}}
			return a.ExtractAgentSessionReferences(opt)
		},
	}
	from.Flags().StringVar(&opt.Sessions, "sessions", "", "explicit source agentsessions host:port")
	from.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified source TLS")
	from.Flags().StringVar(&opt.Assertions, "assertions", "", "authored assertion-only YAML applied to each selected UID")
	from.Flags().StringVar(&opt.Out, "out", "", "private reference manifest path (existing file means append)")
	for _, name := range []string{"sessions", "assertions", "out"} {
		_ = from.MarkFlagRequired(name)
	}
	_ = from.RegisterFlagCompletionFunc("sessions", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
	group.AddCommand(diff, from)
	return group
}
