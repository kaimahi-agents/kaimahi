package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Create, inspect, edit, and chat with agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newAgentListCommand(state), newAgentCreateCommand(state), newAgentEditCommand(state), newAgentChatCommand(state))
	return group
}

func newAgentListCommand(state *commandState) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "list", Short: "List agents and active wiring", Args: cobra.NoArgs}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json|yaml")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json", "yaml"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ListAgents(output) })
	return cmd
}

func newAgentCreateCommand(state *commandState) *cobra.Command {
	var opt app.CreateOptions
	cmd := &cobra.Command{Use: "create [name]", Short: "Scaffold and optionally apply an Agent", Args: usageArgs(0, 1, "kmx agent create [<name>] [flags]")}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "Agent namespace (default kagent)")
	cmd.Flags().StringVar(&opt.Description, "description", "", "one-line description")
	cmd.Flags().StringVar(&opt.ModelConfig, "model", "", "ModelConfig to think with")
	cmd.Flags().StringVar(&opt.Instructions, "instructions", "", "file containing the system message")
	cmd.Flags().StringVar(&opt.Tools, "tools", "", "MCP wiring: <server>:<tool>[,<tool>...]")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write the manifest and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation without applying")
	cmd.Flags().StringVar(&opt.Image, "image", "", "run this image, serving A2A on :8080, instead of a declarative agent")
	cmd.Flags().StringVar(&opt.Isolation, "isolation", "", "placement profile for a bring-your-own agent: virtual-node | none")
	// Asked for rather than probed: kmx cannot see inside a bring-your-own
	// image, and a guessed UID fails the pod at CreateContainer with a
	// message that never names the image. Absent is allowed and says so.
	cmd.Flags().StringVar(&opt.RunAsUser, "run-as-user", "", "UID the --image runs as, or \"root\" to say it needs root")
	_ = cmd.RegisterFlagCompletionFunc("isolation", staticCompletion([]string{"virtual-node", "none"}))
	cmd.RunE = appRun(state, func(a *app.App) error {
		if len(cmd.Flags().Args()) == 0 {
			return a.CreateAgentInteractive(opt)
		}
		opt.Name = cmd.Flags().Arg(0)
		return a.CreateAgent(opt)
	})
	return cmd
}

func newAgentEditCommand(state *commandState) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "edit <name>", Short: "Edit and validate local Agent source", Args: usageArgs(1, 1, "kmx agent edit <name> [--file <path>]")}
	cmd.Flags().StringVar(&file, "file", "", "local Agent manifest")
	cmd.ValidArgsFunction = completeLocalAgents
	cmd.RunE = appRun(state, func(a *app.App) error { return a.EditAgent(cmd.Flags().Arg(0), file) })
	return cmd
}

func newAgentChatCommand(state *commandState) *cobra.Command {
	var asJSON, interactive bool
	var session string
	cmd := &cobra.Command{Use: "chat <name> [message...]", Short: "Chat with an Agent", Args: usageArgs(1, -1, "kmx agent chat [--json] [--interactive] [--session <id>] <name> [message]")}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw A2A task")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "keep one streamed session open")
	cmd.Flags().StringVar(&session, "session", "", "resume this kagent session")
	cmd.ValidArgsFunction = completeLiveAgents
	cmd.RunE = appRun(state, func(a *app.App) error {
		args := cmd.Flags().Args()
		a.ChatJSON(asJSON)
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Interactive: interactive, Session: session})
	})
	return cmd
}
