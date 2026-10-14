package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// newAgentShowCommand answers one question: will this agent work, and if
// not, which hop is broken.
//
// `list` is the inventory and this is the chain. They are separate verbs
// because an Agent's own object says nothing about the Provider that refuses
// its model calls, and joining three kinds by hand is the assembly an
// operator gets wrong under pressure.
func newAgentShowCommand(state *commandState) *cobra.Command {
	var opt app.ShowOptions
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show an Orka Agent and the chain it depends on",
		Args:  usageArgs(1, 1, "kmx orka show <name> --namespace <namespace>"),
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "namespace the Orka controller watches (required)")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	cmd.Flags().IntVar(&opt.Tasks, "tasks", 5, "how many recent Tasks to read back")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ShowAgent(cmd.Flags().Arg(0), opt) })
	return cmd
}

func newAgentListCommand(state *commandState) *cobra.Command {
	var output, namespace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the Orka Agents in a namespace",
		Long: `List Orka Agent resources.

These are live Agent resources; kmx orka show inspects their dependencies.

--namespace selects the namespace the Orka controller watches. It defaults to
the namespace the pinned installer uses, which is the same default kmx chat
resolves against.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json|yaml")
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace the Orka controller watches (default: "+app.OrkaNamespace+")")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json", "yaml"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ListAgents(output, namespace) })
	return cmd
}

func newAgentCreateCommand(state *commandState) *cobra.Command {
	var root, name, description, model, instructions, runtimeID, version, engine string
	var profile workspace.RuntimeProfile
	cmd := &cobra.Command{
		Use: "create <name>", Short: "Author an offline workspace member",
		Long:    "Author a tool-free Orka, exact kagent Go or pinned AgentSessions chat member in a local workspace. AgentSessions evaluation uses an explicit development host; its lift, chat and status remain unavailable.\n\nCreate does not evaluate, publish, deploy or load cluster configuration.\nUse eval, push and immutable digest lift with explicit evidence for deployment.",
		Example: "  kmx create greeter --workspace ./greeter --model qwen-3.5-2b --instructions instructions.md",
	}
	cmd.Flags().StringVar(&root, "workspace", "", "select an exact workspace directory (initialize without prompting if absent)")
	_ = cmd.MarkFlagDirname("workspace")
	cmd.Flags().StringVar(&description, "description", "", "one-line description")
	cmd.Flags().StringVar(&model, "model", "", "authored model identifier (required)")
	cmd.Flags().StringVar(&instructions, "instructions", "", "file containing the system message (required)")
	cmd.Flags().StringVar(&runtimeID, "runtime", "orka", "agent runtime: orka, kagent or agentsessions")
	cmd.Flags().StringVar(&version, "runtime-version", "v0.10.3", "exact workspace runtime profile (kagent: v0.10.3; agentsessions: pinned commit)")
	cmd.Flags().StringVar(&engine, "kagent-runtime", "go", "kagent declarative runtime: go")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"orka", "kagent", "agentsessions"}))
	_ = cmd.RegisterFlagCompletionFunc("runtime-version", staticCompletion([]string{"v0.10.3", workspace.AgentSessionsVersion}))
	_ = cmd.RegisterFlagCompletionFunc("kagent-runtime", staticCompletion([]string{"go"}))
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("workspace") && strings.TrimSpace(root) == "" {
			return fmt.Errorf("workspace creation requires a non-empty --workspace directory")
		}
		if err := validateWorkspaceCreateFlags(cmd, state); err != nil {
			return err
		}
		if len(args) != 1 {
			return fmt.Errorf("workspace creation requires a name: kmx create <name> --model <model> --instructions <file>")
		}
		name = args[0]
		profile = workspace.RuntimeProfile{ID: runtimeID}
		if runtimeID == "kagent" {
			if version != "v0.10.3" || engine != "go" {
				return fmt.Errorf("workspace kagent supports only --runtime-version v0.10.3 and --kagent-runtime go")
			}
			profile.Version, profile.Engine = version, engine
		} else if runtimeID == "agentsessions" {
			if (cmd.Flags().Changed("runtime-version") && version != workspace.AgentSessionsVersion) || cmd.Flags().Changed("kagent-runtime") {
				return fmt.Errorf("workspace agentsessions requires the pinned --runtime-version %s and refuses --kagent-runtime", workspace.AgentSessionsVersion)
			}
			profile.Version, profile.Engine = workspace.AgentSessionsVersion, "chat"
		} else if runtimeID != "orka" || cmd.Flags().Changed("runtime-version") || cmd.Flags().Changed("kagent-runtime") {
			return fmt.Errorf("workspace --runtime must be orka without version/engine or exact kagent v0.10.3 go")
		}
		if strings.TrimSpace(model) == "" || strings.TrimSpace(instructions) == "" {
			return fmt.Errorf("workspace creation requires --model and --instructions <file>")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Stdin: state.deps.stdin}
		return a.CreateSuiteMember(cmd.Context(), root, workspace.CreateOptions{Name: name, Description: description, Model: model, InstructionsFile: instructions, Runtime: profile})
	}
	return cmd
}

func newAgentChatCommand(state *commandState) *cobra.Command {
	var verbose bool
	var runtime, namespace string
	usage := "kmx chat [--namespace <namespace>] <name> [message]"
	cmd := &cobra.Command{Use: "chat <name> [message...]", Short: "Chat with an Orka Agent", Long: "Open an interactive chat session with an Orka Agent. An optional message is sent as the first turn; for a one-shot Task use kmx run.", Args: usageArgs(1, -1, usage)}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show chat WORKING and TIMING details")
	cmd.Flags().StringVar(&runtime, "runtime", "auto", "agent runtime: auto (detect the Orka Agent) or orka")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Orka Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().String("session", "", "retired server-side session flag")
	cmd.Flags().Bool("json", false, "retired raw A2A task flag")
	_ = cmd.Flags().MarkHidden("session")
	_ = cmd.Flags().MarkHidden("json")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"auto", "orka"}))
	cmd.ValidArgsFunction = completeLiveAgents
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("session") {
			return fmt.Errorf("--session is retired: Orka chat has no resumable server-side session; use kmx chat --namespace <ns> <name>")
		}
		if cmd.Flags().Changed("json") {
			return fmt.Errorf("--json is retired: there is no raw A2A JSON equivalent for an existing Agent; use kmx chat --namespace <ns> <name> for interactive chat")
		}
		return usageArgs(1, -1, usage)(cmd, args)
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		args := cmd.Flags().Args()
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Verbose: verbose, Runtime: runtime, Namespace: namespace})
	})
	return cmd
}
