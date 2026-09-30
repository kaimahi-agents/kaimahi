package main

import (
	"fmt"
	"strings"

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
	cmd.Flags().StringVar(&opt.RequireEvaluated, "require-evaluated", "", "require a passing evaluation receipt for this context label's recorded cluster and namespace")
	cmd.Flags().StringVar(&opt.OverrideGate, "override-gate", "", "lift despite a failed evaluation gate and record this reason in the lift receipt")
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "inspect and report intended reconciliation without writing")
	run := appRun(state, func(a *app.App) error {
		opt.BundleDir = cmd.Flags().Arg(0)
		return a.LiftAgentBundle(opt)
	})
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		for _, flag := range []struct{ name, value string }{{"require-evaluated", opt.RequireEvaluated}, {"override-gate", opt.OverrideGate}} {
			if cmd.Flags().Changed(flag.name) && strings.TrimSpace(flag.value) == "" {
				return fmt.Errorf("--%s requires a non-empty value", flag.name)
			}
		}
		return run(cmd, args)
	}
	return cmd
}
