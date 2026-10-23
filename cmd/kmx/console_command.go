package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newConsoleCommand(state *commandState) *cobra.Command {
	var opt app.AgentTUIOptions
	cmd := &cobra.Command{
		Use: "console", Short: "Open the interactive workspace for agents and environments", Args: cobra.NoArgs,
		Long: "Browse agents by type: local md definitions and Orka Agents in local/remote Kubernetes environments.\nMD agents offer inspect, chat, edit definition/tests and test actions.\nOrka creation reviews source/all cases and a Ready openai Provider before full eval, publication and Deploy.\nUse h/j/k/l or arrows; /env selects environments and /inspect shows the selected agent.\nR opens recent read-only Orka runs (caller must list/get Tasks and get the kube-system Namespace).\nDeploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …\nUse --demo for sample data without cluster access.",
	}
	cmd.Flags().StringVar(&opt.LocalContext, "local-context", "", "local kind context (default: last TUI selection, configured context, or first local)")
	cmd.Flags().StringVar(&opt.RemoteContext, "remote-context", "", "remote context (default: last TUI selection, last deploy target, configured context, or first remote)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", app.OrkaNamespace, "Orka namespace to display")
	cmd.Flags().BoolVar(&opt.Demo, "demo", false, "use sample agents; no cluster or cloud operations")
	cmd.Flags().StringVar(&opt.Directory, "directory", ".", "project directory containing md agents")
	cmd.Flags().BoolVar(&opt.Definitions, "definitions", false, "browse source files instead of tracked local instances")
	cmd.Flags().StringVar(&opt.Type, "type", "", "filter agent type: md or orka")
	_ = cmd.RegisterFlagCompletionFunc("type", staticCompletion([]string{"md", "orka"}))
	_ = cmd.RegisterFlagCompletionFunc("local-context", completeContexts)
	_ = cmd.RegisterFlagCompletionFunc("remote-context", completeContexts)
	cmd.RunE = appRun(state, func(a *app.App) error { return a.AgentTUI(opt) })
	return cmd
}
