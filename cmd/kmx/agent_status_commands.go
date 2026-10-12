package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentStatusCommand(state *commandState) *cobra.Command {
	var opt app.BundleStatusOptions
	cmd := &cobra.Command{
		Use:   "status <bundle-dir>",
		Short: "Report Orka known targets or one explicitly selected Kagent target, without writing anything",
		Args:  usageArgs(1, 1, "kmx agent status <bundle-dir> [--to-context <ctx>] [--to-namespace <ns>] [-o table|json]"),
	}
	// --to-context, not --context: the root --context flag selects kmx's own
	// ambient invocation context, but a bundle's destinations are independent
	// of it — the same distinction `kmx agent lift --to-context` already makes.
	cmd.Flags().StringVar(&opt.Runtime, "runtime", "", "runtime (default: orka; kagent requires exact --runtime-version)")
	cmd.Flags().StringVar(&opt.RuntimeVersion, "runtime-version", "", "explicit runtime version (kagent: v0.10.3)")
	cmd.Flags().StringVar(&opt.Bindings, "bindings", "", "KagentBindings file (default: bundle bindings.yaml); repeat external lift bindings explicitly")
	cmd.Flags().StringVar(&opt.Context, "to-context", "", "inspect this destination context (Orka default: every recorded target; Kagent: always explicit)")
	cmd.Flags().StringVar(&opt.Namespace, "to-namespace", "", "destination namespace (Orka: unique recorded namespace or orka-system; Kagent: must match KagentBindings)")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.BundleStatus(opt)
	})
	return cmd
}
