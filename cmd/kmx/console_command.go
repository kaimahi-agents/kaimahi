package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newConsoleCommand(state *commandState) *cobra.Command {
	var opt app.AgentTUIOptions
	cmd := &cobra.Command{
		Use: "console", Short: "Open the interactive workspace for agents and environments", Args: cobra.NoArgs,
		Long: "Browse local Markdown agents and native Orka Agents side by side.\nMD actions create/edit definitions, companions, tests and reviewed tool approvals.\nCluster creation reviews authored source and cases before evaluation and Deploy.\nUse arrows or h/j/k/l; Enter opens actions, n creates locally or reviews a workspace.\nR opens recent read-only Orka runs.\nDeploy moved: kmx package deploy <ref>@<digest> --to-context … --evidence …\nUse --demo for sample data without effects.",
	}
	cmd.Long += "\nRun inspection requires list/get Tasks and get the kube-system Namespace."
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
