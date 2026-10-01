package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newAgentRetireCommand(state *commandState) *cobra.Command {
	var opt app.RetireAgentBundleOptions
	cmd := &cobra.Command{
		Use:   "retire <bundle-dir>",
		Short: "Remove or release a bundle's owned Agent and rendered Provider on a target",
		Args:  usageArgs(1, 1, "kmx agent retire <bundle-dir> [--to-context <ctx>] [--to-namespace <ns>] [--plan] [--delete-adopted]"),
	}
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination context (default: bundle's remembered target)")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", "", "destination namespace (default: remembered or uniquely recorded namespace, else orka-system)")
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "show deletions, releases, or refusals without writing")
	cmd.Flags().BoolVar(&opt.DeleteAdopted, "delete-adopted", false, "also delete adopted and legacy objects instead of releasing them")
	cmd.RunE = appRun(state, func(a *app.App) error { opt.BundleDir = cmd.Flags().Arg(0); return a.RetireAgentBundle(opt) })
	return cmd
}
