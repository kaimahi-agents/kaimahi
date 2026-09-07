package main

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newToolsCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "tools", Short: "Manage governed MCP tool access", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newToolsAddCommand(state), newToolsGovernCommand(state), newToolsUngovernCommand(state), newToolsAllowCommand(state), newToolsAllowlistCommand(state), newToolsSandboxCommand(state))
	return group
}

// newToolsSandboxCommand installs the WASM runtime a tool server can opt
// into. Installing it sandboxes nothing on its own: an MCP server has to
// set runtimeClassName before any call lands there, which is what
// `kmx tools sandbox status` counts.
//
// It belongs to the `tools` family because it governs what this family
// governs — what a tool call may do — at the runtime boundary rather than the
// gateway's allowlist.
//
// It deliberately registers NO --credential. The sandbox is a node-level
// runtime: installed once per cluster, and every governed tool call lands on
// it whatever credential made it. A flag accepted here and then ignored is how
// an operator comes to believe they scoped something they did not.
func newToolsSandboxCommand(state *commandState) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Install the WASM runtime a tool server can run in",
		Args:  cobra.NoArgs,
	}
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ToolSandbox() })

	status := &cobra.Command{
		Use:   "status",
		Short: "Report whether the sandbox is installed and what uses it",
		Args:  cobra.NoArgs,
	}
	status.RunE = appRun(state, func(a *app.App) error { return a.ToolSandboxStatus() })
	cmd.AddCommand(status)
	return cmd
}

func newToolsAddCommand(state *commandState) *cobra.Command {
	var opt app.AddUpstreamOptions
	cmd := &cobra.Command{Use: "add <name>", Short: "Scaffold and add a governed MCP upstream", Args: usageArgs(1, 1, "kmx tools add <name> --url <url> --tool <tool>:<fields>")}
	cmd.Flags().StringVar(&opt.URL, "url", "", "server's in-cluster MCP endpoint")
	cmd.Flags().StringArrayVar(&opt.Tools, "tool", nil, "tool policy declaration; repeat for each tool")
	cmd.Flags().StringVar(&opt.ServerEgress, "server-egress", "", "none|dns|keep")
	cmd.Flags().IntVar(&opt.PodPort, "pod-port", 0, "container port for named targetPort")
	cmd.Flags().StringVar(&opt.Secret, "secret", "", "agent-side Secret name")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation only")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Name = cmd.Flags().Arg(0)
		if strings.TrimSpace(opt.URL) == "" {
			return errors.New("kmx tools add: --url is required — the server's own in-cluster MCP endpoint")
		}
		return a.AddUpstream(opt)
	})
	return cmd
}

func toolsFlags(cmd *cobra.Command, opt *app.ToolsOptions, govern bool) {
	cmd.Flags().StringVar(&opt.Credential, "credential", "", "gateway credential")
	if govern {
		cmd.Flags().StringVar(&opt.Agent, "agent", "", "agent to repoint")
		cmd.Flags().StringVar(&opt.Secret, "secret", "", "agent-side Secret")
		cmd.Flags().StringVar(&opt.SecretNamespace, "secret-namespace", "", "Secret namespace")
		cmd.Flags().StringVar(&opt.Tools, "tools", "", "comma-separated allowlist ('-' for empty)")
		cmd.Flags().StringVar(&opt.Server, "server", "", "RemoteMCPServer")
	}
}

func newToolsGovernCommand(state *commandState) *cobra.Command {
	var opt app.ToolsOptions
	cmd := &cobra.Command{Use: "govern", Short: "Put an agent behind the MCP gateway", Args: cobra.NoArgs}
	toolsFlags(cmd, &opt, true)
	cmd.RunE = appRun(state, func(a *app.App) error {
		if strings.TrimSpace(opt.Credential) == "" {
			opt.Credential = a.Cfg.ToolsCredential
		}
		return a.GovernTools(opt)
	})
	return cmd
}

func newToolsUngovernCommand(state *commandState) *cobra.Command {
	var opt app.ToolsOptions
	cmd := &cobra.Command{Use: "ungovern", Short: "Restore direct tools-agent wiring", Args: cobra.NoArgs}
	toolsFlags(cmd, &opt, false)
	cmd.RunE = appRun(state, func(a *app.App) error {
		if strings.TrimSpace(opt.Credential) == "" {
			opt.Credential = a.Cfg.ToolsCredential
		}
		return a.UngovernTools(opt)
	})
	return cmd
}

func newToolsAllowCommand(state *commandState) *cobra.Command {
	var credential string
	cmd := &cobra.Command{Use: "allow <tool,tool|->", Short: "Replace the gateway allowlist", Args: usageArgs(1, 1, "kmx tools allow <tool,tool|-> [--credential <name>]")}
	cmd.Flags().StringVar(&credential, "credential", "", "gateway credential")
	cmd.RunE = appRun(state, func(a *app.App) error {
		if strings.TrimSpace(credential) == "" {
			credential = a.Cfg.ToolsCredential
		}
		return a.AllowTools(credential, cmd.Flags().Arg(0))
	})
	return cmd
}

func newToolsAllowlistCommand(state *commandState) *cobra.Command {
	var credential string
	cmd := &cobra.Command{Use: "allowlist [credential]", Short: "Read the gateway allowlist", Args: usageArgs(0, 1, "kmx tools allowlist [<credential>]")}
	cmd.Flags().StringVar(&credential, "credential", "", "gateway credential")
	cmd.RunE = appRun(state, func(a *app.App) error {
		if len(cmd.Flags().Args()) == 1 {
			credential = cmd.Flags().Arg(0)
		}
		if strings.TrimSpace(credential) == "" {
			credential = a.Cfg.ToolsCredential
		}
		return a.ToolAllowlist(credential)
	})
	return cmd
}
