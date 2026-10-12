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
		Short: "Lift an agent bundle: Orka reconcile or explicit Kagent create-only deployment",
		Args:  usageArgs(1, 1, "kmx bundle lift <bundle-dir> --to-context <ctx> --inference provider:<name> [--to-namespace <ns>] [--plan]"),
	}
	cmd.Flags().StringVar(&opt.Runtime, "runtime", "", "runtime (default: orka; kagent requires exact --runtime-version)")
	cmd.Flags().StringVar(&opt.RuntimeVersion, "runtime-version", "", "explicit runtime version (kagent: v0.10.3)")
	cmd.Flags().StringVar(&opt.Bindings, "bindings", "", "KagentBindings file (default: bundle bindings.yaml)")
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "destination kubeconfig context (Orka: remembered after successful lift; Kagent: always explicit)")
	cmd.Flags().StringVar(&opt.ToNamespace, "to-namespace", "", "destination namespace (Orka: remembered or orka-system; Kagent: must match KagentBindings)")
	cmd.Flags().StringVar(&opt.Inference, "inference", "", "Orka only: existing destination inference Provider: provider:<name>")
	cmd.Flags().StringVar(&opt.RequireEvaluated, "require-evaluated", "", "Orka only: require a passing evaluation receipt for this context label's recorded cluster and namespace")
	cmd.Flags().StringVar(&opt.OverrideGate, "override-gate", "", "Orka only: lift despite a failed evaluation gate and record this reason in the lift receipt")
	cmd.Flags().BoolVar(&opt.Plan, "plan", false, "inspect the selected runtime's lift preflights without writing resources or receipts")
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
