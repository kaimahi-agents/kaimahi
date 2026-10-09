package main

import (
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newConsoleCommand(state *commandState) *cobra.Command {
	var opt app.AgentTUIOptions
	var suite app.SuiteConsoleOptions
	var attestations bool
	cmd := &cobra.Command{
		Use: "console", Short: "Open the interactive workspace for agents and environments", Args: cobra.NoArgs,
		Long: "Browse one local kind and one remote Kubernetes environment side by side.\nThe console lists, creates, chats with and edits native Orka Agents only.\nUse h/j/k/l or arrows to navigate; / opens commands and argument completion.\nR opens recent read-only Orka runs for the selected Agent (caller must list/get Tasks and get the kube-system Namespace).\n/lift offers local Orka agents, remote contexts, and a new AKS environment.\nUse --demo to explore with sample data and no cluster access.",
	}
	cmd.Long += "\n\nWith --workspace, use the AgentSuite source/build/publish/lift console instead.\nModel selection comes from the deployment environment; --registry accepts private registries.\nSee docs/agentsuite-image-lift.md for bindings and supported operations."
	cmd.Flags().StringVar(&opt.LocalContext, "local-context", "", "local kind context (default: last TUI selection, configured context, or first local)")
	cmd.Flags().StringVar(&opt.RemoteContext, "remote-context", "", "remote context (default: last TUI selection, last lift target, configured context, or first remote)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", app.OrkaNamespace, "Orka namespace to display")
	cmd.Flags().StringVar(&opt.Bundles, "bundles", "", "directory holding one agent bundle per agent, compared by b (default: agents)")
	cmd.Flags().BoolVar(&opt.Demo, "demo", false, "use sample agents; no cluster or cloud operations")
	cmd.Flags().StringVar(&suite.Workspace, "workspace", "", "open the AgentSuite source/build/lift workspace")
	cmd.Flags().StringVar(&suite.Registry, "registry", "", "AgentSuite image registry/repository")
	cmd.Flags().BoolVar(&suite.PlainHTTP, "plain-http", false, "anonymous local-development registry HTTP")
	cmd.Flags().StringVar(&suite.Build.Builder, "builder", "", "Docker buildx builder for workspace builds")
	cmd.Flags().BoolVar(&attestations, "attestations", true, "request SBOM and provenance for workspace builds")
	cmd.Flags().BoolVar(&suite.Build.RequireAttestations, "require-attestations", false, "require SBOM and provenance in built images")
	_ = cmd.RegisterFlagCompletionFunc("local-context", completeContexts)
	_ = cmd.RegisterFlagCompletionFunc("remote-context", completeContexts)
	cmd.RunE = appRun(state, func(a *app.App) error {
		if suite.Workspace != "" {
			suite.Build.DisableAttestations = !attestations
			if opt.Demo {
				return fmt.Errorf("--workspace performs real operations; omit --demo")
			}
			return a.SuiteConsole(suite)
		}
		return a.AgentTUI(opt)
	})
	return cmd
}
