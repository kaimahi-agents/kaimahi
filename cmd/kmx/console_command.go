package main

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newConsoleCommand(state *commandState) *cobra.Command {
	var opt app.AgentTUIOptions
	cmd := &cobra.Command{
		Use: "console", Short: "Open the interactive workspace for agents and environments", Args: cobra.NoArgs,
		Long: "Browse one local kind and one remote Kubernetes environment side by side.\nThe console lists, creates, chats with and edits native Orka Agents only.\nUse h/j/k/l or arrows to navigate; / opens commands and argument completion.\n/lift offers local Orka agents, remote contexts, and a new AKS environment.\nUse --demo to explore with sample data and no cluster access.",
	}
	cmd.Flags().StringVar(&opt.LocalContext, "local-context", "", "local kind context (default: last TUI selection, configured context, or first local)")
	cmd.Flags().StringVar(&opt.RemoteContext, "remote-context", "", "remote context (default: last TUI selection, last lift target, configured context, or first remote)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", app.OrkaNamespace, "Orka namespace to display")
	cmd.Flags().BoolVar(&opt.Demo, "demo", false, "use sample agents; no cluster or cloud operations")
	_ = cmd.RegisterFlagCompletionFunc("local-context", completeContexts)
	_ = cmd.RegisterFlagCompletionFunc("remote-context", completeContexts)
	cmd.RunE = appRun(state, func(a *app.App) error { return a.AgentTUI(opt) })
	return cmd
}
