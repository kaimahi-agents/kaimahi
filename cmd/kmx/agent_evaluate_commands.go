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
		Short: "Run a bundle's eval/ cases on Orka or agentsessions and record a receipt",
		Long: "Run each eval/*.yaml case as one Orka Task against the Agent deployed at the destination,\n" +
			"but only when that Agent carries the bundle's current portable digest. Answers are printed;\n" +
			"the receipt in receipts/ records verdicts and answer digests, never answer text.\n" +
			"With --sessions <host:port>, run each case as a new chat session instead, with the bundle's\n" +
			"exact instructions and the host's model. This requires agentsessions system_prompt support.\n" +
			"Non-loopback hosts require verified TLS; --sessions-ca supplies a private CA.\n" +
			"llmJudge uses the authored judgeModel/judgeVersion, never the agent model. Configure an\n" +
			"explicit --judge-base-url and optionally --judge-key-env (environment variable name only).\n" +
			"Two temperature 0 judgments must both meet minScore to pass or both fall below to fail;\n" +
			"split/unavailable results are unknown. The case deadline includes judging; no retries.\n" +
			"Input, answer and rubric go to the judge provider. Safe reasoning is printed to stdout,\n" +
			"never stored in receipts; offline replay does not verify judge outcomes.\n" +
			"Exits non-zero unless every case passed. Sessions receipts do not satisfy lift gates yet.",
		Args: usageArgs(1, 1, "kmx agent evaluate <bundle-dir> [--to-context <ctx> | --sessions <host:port>] [--case <id>]"),
	}
	// --to-context, not --context, for the same reason as lift and status.
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination kubeconfig context (default: the bundle's remembered lift target)")
	cmd.Flags().StringVar(&opt.Sessions, "sessions", "", "agentsessions host:port with the chat harness (instead of a deployed Orka target)")
	cmd.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified TLS to the sessions host (loopback uses TLS when supplied)")
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
		for _, name := range []string{"sessions", "sessions-ca", "judge-base-url", "judge-key-env"} {
			if cmd.Flags().Changed(name) {
				value, _ := cmd.Flags().GetString(name)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s requires a non-empty value", name)
				}
			}
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
