package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentLiftCommand(state *commandState) *cobra.Command {
	var opt app.LiftAgentBundleOptions
	cmd := &cobra.Command{
		Use:   "lift <bundle-dir>",
		Short: "Reconcile an agent bundle onto an explicitly selected destination",
		Args:  usageArgs(1, 1, "kmx agent lift <bundle-dir> --to-context <ctx> --inference provider:<name> [--to-namespace <ns>] [--plan]"),
	}
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination kubeconfig context (remembered per bundle after a successful lift)")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", "", "destination namespace (default: orka-system)")
	cmd.Flags().StringVar(&opt.Inference, "inference", "", "existing destination inference Provider: provider:<name>")
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "inspect and report intended reconciliation without writing")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.LiftAgentBundle(opt)
	})
	return cmd
}
