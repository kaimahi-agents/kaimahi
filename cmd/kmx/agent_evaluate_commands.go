package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func newAgentEvaluateCommand(state *commandState) *cobra.Command {
	var opt app.EvaluateAgentBundleOptions
	cmd := &cobra.Command{
		Use:   "evaluate <bundle-dir>",
		Short: "Run a bundle's eval/ cases on Orka, kagent or agentsessions",
		Long: "Run each eval/*.yaml case as one Orka Task against the Agent deployed at the destination,\n" +
			"but only when that Agent carries the bundle's current portable digest. Answers are printed;\n" +
			"the receipt in receipts/ records verdicts and answer digests, never answer text.\n" +
			"With --runtime kagent --runtime-version v0.10.3, evaluate a receipt-bound Go Agent\n" +
			"using explicit --to-context and KagentBindings. Each case sends one fresh blocking A2A Task.\n" +
			"auth: unsecure only, trusted-proxy refused. Python and promotion gates remain unsupported.\n" +
			"kagent retains full prompts, answers and tool history in its database; audit may retain bodies.\n" +
			"With --sessions <host:port>, run each case as a new chat session instead, with the bundle's\n" +
			"exact instructions and the host's model. This requires agentsessions system_prompt support.\n" +
			"Non-loopback hosts require verified TLS; --sessions-ca supplies a private CA.\n" +
			"llmJudge uses the authored judgeModel/judgeVersion, never the agent model. Configure an\n" +
			"explicit --judge-base-url and optionally --judge-key-env (environment variable name only).\n" +
			"Two temperature 0 judgments must both meet minScore to pass or both fall below to fail;\n" +
			"split/unavailable results are unknown. The case deadline includes judging; no retries.\n" +
			"Input, answer and rubric go to the judge provider. Safe reasoning is printed to stdout,\n" +
			"never stored in receipts; offline replay does not verify judge outcomes.\n" +
			"With --from-sessions <manifest>, resolve recorded inputs from explicit --source-sessions.\n" +
			"Recorded inputs will be sent to --sessions and retained in candidate journals: consent first\n" +
			"with KAIMAHI_CONFIRM=<candidate-host:port>, even on loopback. Judges additionally require\n" +
			"KAIMAHI_CONFIRM_JUDGE=<canonical-judge-base-url> before input/answer/rubric transfer.\n" +
			"Reference runs print verdicts, IDs and digests only; --show-content explicitly prints answers\n" +
			"and judge reasoning. Missing/expired/unsupported sources are unknown, never skipped.\n" +
			"Exits non-zero unless every case passed. Sessions receipts do not satisfy lift gates yet.",
		Args: usageArgs(1, 1, "kmx bundle evaluate <bundle-dir> [--to-context <ctx> | --sessions <host:port>] [--case <id>]"),
	}
	// --to-context, not --context, for the same reason as lift and status.
	cmd.Flags().StringVar(&opt.Runtime, "runtime", "", "runtime (default: orka; kagent requires exact --runtime-version)")
	cmd.Flags().StringVar(&opt.RuntimeVersion, "runtime-version", "", "explicit kagent evaluation version: v0.10.3")
	cmd.Flags().StringVar(&opt.Bindings, "bindings", "", "KagentBindings file (default: bundle bindings.yaml)")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", "", "kagent namespace; must agree with KagentBindings")
	_ = cmd.RegisterFlagCompletionFunc("runtime", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"orka", "kagent"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("runtime-version", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"v0.10.3"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination kubeconfig context (kagent: explicit; Orka: remembered lift target by default)")
	cmd.Flags().StringVar(&opt.Sessions, "sessions", "", "agentsessions host:port with the chat harness (instead of a deployed Orka target)")
	cmd.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified TLS to the sessions host (loopback uses TLS when supplied)")
	cmd.Flags().StringVar(&opt.FromSessions, "from-sessions", "", "private reference manifest instead of bundle eval/ inputs")
	cmd.Flags().StringVar(&opt.SourceSessions, "source-sessions", "", "explicit source host:port matching the reference manifest")
	cmd.Flags().StringVar(&opt.SourceSessionsCA, "source-sessions-ca", "", "PEM CA file for verified source TLS")
	cmd.Flags().BoolVar(&opt.ShowContent, "show-content", false, "explicitly print reference-run answers and judge reasoning (sensitive)")
	cmd.MarkFlagsRequiredTogether("from-sessions", "source-sessions")
	_ = cmd.RegisterFlagCompletionFunc("source-sessions", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&opt.JudgeBaseURL, "judge-base-url", "", "explicit OpenAI-compatible judge base URL (HTTPS except keyless literal loopback)")
	cmd.Flags().StringVar(&opt.JudgeKeyEnv, "judge-key-env", "", "environment variable name containing the judge credential (HTTPS only)")
	for _, name := range []string{"judge-base-url", "judge-key-env"} {
		_ = cmd.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		})
	}
	cmd.Flags().StringVar(&opt.Case, "case", "", "run only the case with this id")
	cmd.Flags().DurationVar(&opt.CaseTimeout, "case-timeout", 0, "time each case may take to reach a readable terminal result (default 5m, max 9m)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	runOrka := appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.EvaluateAgentBundle(opt)
	})
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		for _, name := range []string{"runtime", "runtime-version", "bindings", "to-namespace", "to-context", "sessions", "sessions-ca", "judge-base-url", "judge-key-env", "from-sessions", "source-sessions", "source-sessions-ca"} {
			if cmd.Flags().Changed(name) {
				value, _ := cmd.Flags().GetString(name)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s requires a non-empty value", name)
				}
			}
		}
		if opt.Runtime != "kagent" && (opt.RuntimeVersion != "" || opt.Bindings != "" || opt.ToNamespace != "") {
			return fmt.Errorf("--runtime-version, --bindings and --to-namespace require explicit --runtime kagent")
		}
		if opt.Runtime == "kagent" {
			if opt.Sessions != "" || opt.SessionsCA != "" || opt.FromSessions != "" || opt.SourceSessions != "" || opt.SourceSessionsCA != "" || cmd.Flags().Changed("show-content") {
				return fmt.Errorf("kagent evaluation cannot be combined with sessions or recorded-session options")
			}
			if opt.RuntimeVersion != "v0.10.3" {
				return fmt.Errorf("kagent evaluation requires explicit --runtime-version v0.10.3")
			}
			if cmd.Flags().Changed("result-port") {
				return fmt.Errorf("--result-port is only for Orka evaluation")
			}
			opt.ResultPort = ""
		}
		if opt.Sessions != "" && cmd.Flags().Changed("runtime") {
			return fmt.Errorf("--sessions cannot be combined with --runtime")
		}
		if opt.FromSessions == "" && (cmd.Flags().Changed("source-sessions") || cmd.Flags().Changed("source-sessions-ca") || cmd.Flags().Changed("show-content")) {
			return fmt.Errorf("--source-sessions, --source-sessions-ca and --show-content require --from-sessions")
		}
		if opt.FromSessions != "" && (opt.Sessions == "" || opt.SourceSessions == "") {
			return fmt.Errorf("--from-sessions requires --sessions and --source-sessions")
		}
		if opt.Sessions != "" {
			if cmd.Flags().Changed("to-context") {
				return fmt.Errorf("--sessions cannot be combined with --to-context")
			}
			if cmd.Flags().Changed("result-port") {
				return fmt.Errorf("--result-port is only for Orka evaluation")
			}
			opt.ResultPort = ""
		}
		if opt.SessionsCA != "" && opt.Sessions == "" {
			return fmt.Errorf("--sessions-ca requires --sessions")
		}
		if opt.Sessions != "" {
			// Cluster configuration is irrelevant to this explicit sessions host.
			// Preserve command cancellation without loading kubeconfig preferences.
			opt.BundleDir = cmd.Flags().Arg(0)
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Run: &run.Runner{Context: cmd.Context()}}
			return a.EvaluateAgentBundle(opt)
		}
		return runOrka(cmd, args)
	}
	return cmd
}
