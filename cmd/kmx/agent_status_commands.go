package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentStatusCommand(state *commandState) *cobra.Command {
	var opt app.BundleStatusOptions
	cmd := &cobra.Command{
		Use:   "status <bundle-dir>",
		Short: "Report an agent bundle's deployment state across its known targets, without writing anything",
		Args:  usageArgs(1, 1, "kmx agent status <bundle-dir> [--to-context <ctx>] [--to-namespace <ns>] [-o table|json]"),
	}
	// --to-context, not --context: the root --context flag selects kmx's own
	// ambient invocation context, but a bundle's destinations are independent
	// of it — the same distinction `kmx agent lift --to-context` already makes.
	cmd.Flags().StringVar(&opt.Context, "to-context", "", "inspect only this destination context (default: every recorded target)")
	cmd.Flags().StringVar(&opt.Namespace, "to-namespace", "", "destination namespace for --to-context (default: this bundle's own unique recorded namespace, or orka-system)")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.BundleStatus(opt)
	})
	return cmd
}
