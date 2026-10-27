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

func newAgentTestCommand(state *commandState) *cobra.Command {
	var file fileCommandOptions
	cmd := &cobra.Command{
		Use: "test FILE", Short: "Test a markdown agent file's saved examples",
		Long: "Test FILE checks every saved example; --case selects one example.\n" +
			"Exit: 0 pass, 1 assertion fail, 2 unknown/execution error, 130 interrupted.\n" +
			"Use test explain --file FILE for retained diagnostics.\n" +
			"Local preview is not package evidence. Content retention and privacy:\n" +
			previewCaveatsURL,
		Example: "  kmx test greeter.agent.md\n  kmx test greeter.agent.md --case greeting\n  kmx test greeter.agent.md -o json --no-content",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fileUsageError(fmt.Errorf("usage: kmx test FILE; workspace evaluation moved to kmx package test"))
			}
			switch args[0] {
			case "cleanup", "diff", "export", "verify", "from-sessions":
				return fileUsageError(fmt.Errorf("workspace helper moved to kmx package test %s; use an explicit path for a file with that name", args[0]))
			}
			return fileUsageError(validateFileCommand(cmd, state, file))
		},
		PreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFileCommand(cmd, state, args[0], file, "", "")
		},
	}
	registerFileCommandFlags(cmd, &file, true)
	explain := newEvalExplainCommand(state)
	explain.Use = "explain [failure-id] --file FILE"
	explain.Short = "Inspect or clear retained file Test failures offline"
	explain.Long = "Inspect retained Test failures for FILE without model or cluster access.\n" +
		"Content is hidden unless --show-content is explicit.\n" +
		"--clear removes diagnostics, not journals or source history.\n" +
		"Content retention and privacy:\n" + previewCaveatsURL
	explain.Example = "  kmx test explain --file greeter.agent.md\n  kmx test explain --file greeter.agent.md --clear"
	_ = explain.Flags().MarkHidden("workspace")
	explain.Flags().Lookup("file").Usage = "Agent file whose failures to inspect"
	explain.Flags().Lookup("show-content").Usage = "Show bounded, sanitized failure content"
	explain.Flags().Lookup("clear").Usage = "Clear one ID or this file's retained failures"
	explain.SetHelpFunc(shortCommandHelp)
	explain.PreRunE = func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("file") || cmd.Flags().Changed("workspace") {
			return fmt.Errorf("test explain requires --file FILE; workspace diagnostics moved to kmx package test explain")
		}
		return nil
	}
	cmd.AddCommand(explain)
	return cmd
}

func newAgentEvalCommand(state *commandState) *cobra.Command {
	cmd := newAgentEvaluationCommand(state, "eval")
	cmd.Hidden = true
	addLegacyEvalNotice(cmd, state.noticeLegacyEval)
	return cmd
}

func newAgentEvaluationCommand(state *commandState, verb string) *cobra.Command {
	native := app.SuiteWorkspaceEvaluationOptions{ManagedPass: true}
	var destination string
	var output string
	commandPath := verb
	if verb == "test" {
		commandPath = "package test"
	}
	group := &cobra.Command{
		Use: verb, Short: "Evaluate a native workspace or export, verify and compare saved evidence",
		Long: "Evaluate every authored case in one packaged tool-free native Orka or exact v0.10.3 Go kagent workspace, discovered from the nearest ancestor workspace.yaml or selected exactly with --workspace.\n" +
			"--to NAME resolves copied selectors from workspace-root kmx.yaml; explicit flags override. Without --to-context or --development-host, evalTarget may select a named destination. No remembered/current-context fallback.\n" +
			"Orka requires --inference provider:NAME and result-access ServiceAccount/RBAC. Kagent requires author-owned --bindings FILE, binding namespace and unsecure auth; trusted-proxy and --result-port refuse. No runtime or model override or subset evaluation. llmJudge requires an explicit --judge-base-url and authored judgeModel/judgeVersion; --judge-key-env names an optional HTTPS credential variable. Input, answer and rubric transfer to that provider for two independent samples per assertion; disagreement/error is unknown. Failure summaries show assertion IDs/types and judge scores; --show-content additionally prints sanitized bounded answer excerpts, expected values and judge rationale on stderr. Non-pass runs are retained privately under KMX_HOME (last 10 per workspace across revisions); use package test explain to inspect or clear them offline. Content is diagnostic-only, never evidence.\n" +
			"Packaged AgentSessions requires --development-host FILE: an explicit private operator observation of a pinned built-in chat host on literal loopback, unauthenticated, and --bindings FILE: an AgentSessionsBinding whose model.name is exactly the packaged model. Evidence records the binding digest, which covers every judged or repeated run; the host's endpoint is not attested. It refuses all cluster/inference/result selectors and loads no cluster configuration. Authored llmJudge uses the same explicit judge transport options and transfers case inputs, attributed answers and rubrics.\n" +
			"Development journals retain instructions, inputs, answers and per-exec config in cleartext under operator-managed retention. This is not authenticated remote promotion.\n" +
			"Public Orka uses fresh digest/run-scoped candidates, never adoption/update/reuse, and exports passing evidence only after verified cleanup and final source/shared-input checks. Startup reports leftovers without deleting or blocking a fresh evaluation. Use package test cleanup [full-run-id] for explicit confirmed recovery; unanchored orphans refuse. Private Quickstart/console and other runtimes are unchanged.\n" +
			"Cases optionally declare runs/minPasses (default one run; omitted threshold requires all runs). Each run submits a fresh Task or AgentSessions Session/config/Exec, never retried, within its own per-run deadline including judging. Repeated cases disclose the total run-time budget in preflight.\n" +
			"Any unknown run vetoes a pass. Execution failure, drift or cancellation aborts without a receipt; resources or Sessions may remain.\n" +
			"Output defaults to text for all runtimes. Orka-only -o json emits one payload-free outcome document on success or execution abort; diagnostics go to stderr. Execution counts use returned TaskUIDs, not assertion completion; total remains planned.\n" +
			"Completed passing metadata is registered in private KMX state after successful evaluation, Orka cleanup and finalization. --out optionally also writes a new standalone receipt in an existing private directory outside workspace/cache trees. Managed evidence retains only full passing metadata, unsigned and not a policy gate; failure diagnostics are separate.\n" +
			"Case inputs are sent to the runtime and model; upstream storage/audit policy may retain them. Real native CLI execution qualification remains separate from hermetic checks.",
		Example: "  kmx package test\n" +
			"  kmx package test --to dev --out ../private/passing.json\n" +
			"  kmx package test --to-context kind-demo --inference provider:inference --out ../private/passing.json\n" +
			"  kmx package test --workspace ./workspace --to-context remote --to-namespace orka-system --inference provider:inference --out ../private/passing.json",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("native evaluation accepts no positional references")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("eval output must be text or json")
			}
			if err := validateContainerEngineFlag(cmd, args); err != nil {
				return err
			}
			if state.contextFlag != "" || cmd.Flags().Changed("context") {
				return fmt.Errorf("--context cannot be used with native eval; use --to-context")
			}
			if native.DevelopmentHost != "" {
				for _, name := range []string{"to", "to-context", "to-namespace", "inference", "result-port", "container-engine"} {
					if cmd.Flag(name).Changed {
						return fmt.Errorf("--development-host refuses --%s", name)
					}
				}
			}
			for _, name := range []string{"to", "workspace", "to-context", "to-namespace", "inference", "bindings", "development-host", "out", "result-port", "judge-base-url", "judge-key-env"} {
				if flag := cmd.Flag(name); flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("native eval requires non-empty --%s", name)
				}
			}
			if native.CaseTimeout <= 0 {
				return fmt.Errorf("native evaluation requires a positive --case-timeout")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := refuseMultiMemberWorkspace(native.Workspace); err != nil {
				return err
			}
			selectedNative := native
			selectedNative.JSON = output == "json"
			if !cmd.Flags().Changed("to-namespace") {
				selectedNative.ToNamespace = ""
			}
			if !cmd.Flags().Changed("result-port") {
				selectedNative.ResultPort = ""
			}
			selection := destinationSelection{Context: selectedNative.ToContext, Namespace: selectedNative.ToNamespace, Inference: selectedNative.Inference, Bindings: selectedNative.Bindings}
			if selectedNative.DevelopmentHost == "" {
				var root string
				selection, root, err = resolveDestinationSelection(cmd, destination, selectedNative.Workspace, "", true, selection)
				if err != nil {
					return err
				}
				selectedNative.Workspace = root
				selectedNative.ToContext, selectedNative.ToNamespace, selectedNative.ExpectedRuntime = selection.Context, selection.Namespace, selection.ExpectedRuntime
				selectedNative.Inference, selectedNative.Bindings = selection.Inference, selection.Bindings
			}
			if err := displayDestinationSelection(cmd, selection); err != nil {
				return err
			}
			prepared, err := app.PrepareSuiteWorkspaceEvaluation(ctx, selectedNative)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, prepared.Close()) }()
			if selectedNative.DevelopmentHost != "" {
				return (&app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), TestPresentation: verb == "test"}).EvaluateSuiteWorkspace(ctx, prepared)
			}
			selected := *state
			selected.contextFlag = selectedNative.ToContext
			a, err := selected.operationApplication(cmd)
			if err != nil {
				return err
			}
			a.TestPresentation = verb == "test"
			a.Cfg.ContextSource = config.SourceFlag
			if selection.Source != "" && !cmd.Flags().Changed("to-context") {
				a.Cfg.ContextSource = selection.Source
			}
			// Native retry advice must not synthesize the refused global --context.
			parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine), quoteShell(state.executable())}
			for _, arg := range suiteEvaluationRecoveryArgs(state.argv) {
				parts = append(parts, quoteShell(arg))
			}
			a.InvocationCommand = strings.Join(parts, " ")
			return a.EvaluateSuiteWorkspace(ctx, prepared)
		},
	}
	registerDestinationFlag(group, &destination)
	group.Flags().StringVarP(&output, "output", "o", "text", "output: text|json (JSON: Orka only)")
	group.Flags().StringVar(&native.Workspace, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	group.Flags().StringVar(&native.ToContext, "to-context", "", "explicit native evaluation context")
	group.Flags().StringVar(&native.ToNamespace, "to-namespace", app.OrkaNamespace, "native evaluation namespace")
	group.Flags().StringVar(&native.Inference, "inference", "", "existing inference Provider, as provider:NAME")
	group.Flags().StringVar(&native.Bindings, "bindings", "", "author-owned KagentBindings (kagent) or AgentSessionsBinding (agentsessions) file")
	_ = group.MarkFlagFilename("bindings")
	group.Flags().StringVar(&native.DevelopmentHost, "development-host", "", "private JSON observation selecting a loopback unauthenticated AgentSessions development host")
	_ = group.MarkFlagFilename("development-host", "json")
	group.Flags().StringVar(&native.JudgeBaseURL, "judge-base-url", "", "explicit OpenAI-compatible judge endpoint; HTTPS except keyless literal loopback")
	group.Flags().StringVar(&native.JudgeKeyEnv, "judge-key-env", "", "environment variable name containing the judge credential (HTTPS only)")
	for _, name := range []string{"judge-base-url", "judge-key-env"} {
		_ = group.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		})
	}
	group.Flags().BoolVar(&native.ShowContent, "show-content", false, "print sanitized bounded failure excerpts, expected values and judge rationale on stderr (not evidence)")
	group.Flags().StringVar(&native.Out, "out", "", "optional new standalone private receipt outside workspace/cache (managed proof is automatic)")
	group.Flags().DurationVar(&native.CaseTimeout, "case-timeout", 5*time.Minute, "deadline per run, at most 9m")
	group.Flags().StringVar(&native.ResultPort, "result-port", "", "loopback result-forward port (omitted: choose automatically)")
	_ = group.MarkFlagDirname("workspace")
	diff := &cobra.Command{
		Use:   "diff <before.json> <after.json>",
		Short: "Compare assertion outcomes in two saved evaluation receipts",
		Long: "Compare full-passing native Orka/kagent suite evidence without cluster configuration, network access, or model calls.\n" +
			"Reports revision identities and every case/assertion change; never prints answers or assertion operands.\n" +
			"Exits nonzero for regressions, lost or missing evidence, changed sets, or incompatible runtime identities.\n" +
			"A complete stable comparison can exit zero; it does not prove live drift absence.\n" +
			"AgentSessions and legacy receipts are refused; no compatibility reader or conversion is provided.",
		Args: usageArgs(2, 2, "kmx "+commandPath+" diff <before.json> <after.json>"),
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
		Args:              usageArgs(1, 1000, "kmx "+commandPath+" from-sessions <session-uid>... --sessions <source> --assertions <case.yaml> --out <manifest.json>"),
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
	group.AddCommand(diff, from, newEvalCleanupCommand(state, commandPath), newSuiteEvalExportCommand(state), newSuiteEvalVerifyCommand(state), newEvalExplainCommand(state))
	return group
}

const evalDeprecationNotice = "kmx eval is deprecated; use kmx package test (same arguments)."

func (s *commandState) noticeLegacyEval(cmd *cobra.Command) error {
	if s.evalNoticeShown {
		return nil
	}
	s.evalNoticeShown = true
	_, err := fmt.Fprintln(cmd.ErrOrStderr(), evalDeprecationNotice)
	return err
}

// Notices are argument/parse/help concerns, not operational hooks. Each helper
// owns its Args wrapper so required-flag checks and helper-specific hooks cannot
// suppress it. Explicit help shares the same invocation-scoped notice.
func addLegacyEvalNotice(tree *cobra.Command, notice func(*cobra.Command) error) {
	var wrap func(*cobra.Command)
	wrap = func(cmd *cobra.Command) {
		args := cmd.Args
		cmd.Args = func(cmd *cobra.Command, values []string) error {
			var err error
			if args != nil {
				err = args(cmd, values)
			}
			return errors.Join(notice(cmd), err)
		}
		cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
			return errors.Join(notice(cmd), err)
		})
		help := cmd.HelpFunc()
		cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
			_ = notice(cmd)
			help(cmd, args)
		})
		for _, child := range cmd.Commands() {
			wrap(child)
		}
	}
	wrap(tree)
}

// Judge endpoints can be private. Recovery advice keeps access selectors but
// requires operators to supply the endpoint again rather than echoing it.
func suiteEvaluationRecoveryArgs(args []string) []string {
	safe := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--judge-base-url" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--judge-base-url=") {
			continue
		}
		safe = append(safe, args[i])
	}
	return safe
}
