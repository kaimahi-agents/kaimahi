package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentEvaluateCommand(state *commandState) *cobra.Command {
	var opt app.EvaluateAgentBundleOptions
	cmd := &cobra.Command{
		Use:   "evaluate <bundle-dir>",
		Short: "Run a bundle's eval/ cases against its deployed revision and record a receipt",
		Long: "Run each eval/*.yaml case as one Orka Task against the Agent deployed at the destination,\n" +
			"but only when that Agent carries the bundle's current portable digest. Answers are printed;\n" +
			"the receipt in receipts/ records verdicts and answer digests, never answer text.\n" +
			"Exits non-zero unless every case passed.",
		Args: usageArgs(1, 1, "kmx agent evaluate <bundle-dir> [--to-context <ctx>] [--case <id>]"),
	}
	// --to-context, not --context, for the same reason as lift and status.
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination kubeconfig context (default: the bundle's remembered lift target)")
	cmd.Flags().StringVar(&opt.Case, "case", "", "run only the case with this id")
	cmd.Flags().DurationVar(&opt.CaseTimeout, "case-timeout", 0, "time each case may take to reach a readable terminal result (default 5m, max 9m)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.EvaluateAgentBundle(opt)
	})
	return cmd
}
