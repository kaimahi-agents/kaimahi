package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Create, inspect, edit, and chat with agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newAgentListCommand(state), newAgentShowCommand(state), newAgentCreateCommand(state), newAgentEditCommand(state), newAgentChatCommand(state))
	return group
}

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
		Args:  usageArgs(1, 1, "kmx agent show <name> --namespace <namespace>"),
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "namespace the Orka controller watches (required)")
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	cmd.Flags().IntVar(&opt.Tasks, "tasks", 5, "how many recent Tasks to read back")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ShowAgent(cmd.Flags().Arg(0), opt) })
	return cmd
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
	cmd := &cobra.Command{Use: "create [name]", Short: "Create an Orka Agent and Provider, optionally run a Task", Args: usageArgs(0, 1, "kmx agent create [<name>] [flags]"), Long: `Create a declarative Orka Agent and its Provider as reviewable Kubernetes YAML.
Select explicitly the namespace the Orka controller watches. Provider type, model
identifier (not a ModelConfig), and a separately provisioned Secret are required.
This command does not build or deploy application images. Keep your Deployment
or chart; use kmx migrate for an existing application's model seam.
Existing agent chat/edit/list commands remain kagent-specific.

Offline output uses pinned v0.1.3 CRDs (main selects an immutable snapshot), not
cluster admission. Never bulk-apply the bundle or write its value-free Secret
skeleton: create Provider and wait for current-generation Ready, then Agent and
wait, then optionally Task. No Task means no model response was tested.

Applying --task authorizes a model call and needs an explicitly named existing
ServiceAccount. kmx creates no account or permissions. It requests a ten-minute
token; the API server determines the actual granted lifetime. The token has that
account's full effective authority, not result-only scope; discarding it is not
revocation. v0.1.3 authenticates result reads but does not enforce Task
read RBAC; pinned main requires namespaced get on tasks.core.orka.ai. Results use
loopback HTTP through a context-pinned port-forward. Fresh names and UID checks
do not bind returned result bytes to a UID. Dry-run tests neither access nor execution.`}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "explicit namespace the Orka controller watches (required)")
	cmd.Flags().StringVar(&opt.Description, "description", "", "one-line description")
	cmd.Flags().StringVar(&opt.ProviderType, "provider-type", "", "Provider type: openai or anthropic (required)")
	cmd.Flags().StringVar(&opt.Model, "model", "", "Provider model identifier, not a kagent ModelConfig (required)")
	cmd.Flags().StringVar(&opt.Secret, "secret", "", "existing Provider Secret name (required)")
	cmd.Flags().StringVar(&opt.SecretKey, "secret-key", "api-key", "key name within the existing Secret; never a value")
	cmd.Flags().StringVar(&opt.BaseURL, "base-url", "", "optional HTTP(S) Provider endpoint, no credentials/query/fragment")
	cmd.Flags().StringVar(&opt.Instructions, "instructions", "", "file containing the system message")
	cmd.Flags().StringVar(&opt.Tools, "tools", "", "comma-separated explicit Orka tool names (not server:tool)")
	cmd.Flags().StringVar(&opt.Skills, "skills", "", "comma-separated explicit Orka skill names")
	cmd.Flags().StringVar(&opt.Task, "task", "", "first AI Task prompt; applying authorizes execution")
	cmd.Flags().StringVar(&opt.AgentRequestsPerMinute, "agent-requests-per-minute", "", "explicit positive Agent request limit (int32)")
	cmd.Flags().StringVar(&opt.AgentTokensPerMinute, "agent-tokens-per-minute", "", "explicit positive Agent token limit (int64)")
	cmd.Flags().StringVar(&opt.ProviderRequestsPerMinute, "provider-requests-per-minute", "", "explicit positive Provider request limit (int32)")
	cmd.Flags().StringVar(&opt.ProviderTokensPerMinute, "provider-tokens-per-minute", "", "explicit positive Provider token limit (int64)")
	cmd.Flags().StringVar(&opt.SchemaTarget, "schema-target", "", "offline only: v0.1.3 (default) or pinned main")
	cmd.Flags().StringVar(&opt.ResultServiceAccount, "result-service-account", "", "existing ServiceAccount in the selected namespace for Task result access")
	cmd.Flags().StringVar(&opt.OrkaAPIService, "orka-api-service", "orka-api", "Orka API Service name exposing port 8080")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write the manifest and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation and local artifact; no cluster writes or execution")
	_ = cmd.RegisterFlagCompletionFunc("provider-type", staticCompletion([]string{"openai", "anthropic"}))
	_ = cmd.RegisterFlagCompletionFunc("schema-target", staticCompletion([]string{"v0.1.3", "main"}))
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
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		if interactive && asJSON {
			return fmt.Errorf("--interactive and --json cannot be used together")
		}
		return nil
	}
	cmd.ValidArgsFunction = completeLiveAgents
	cmd.RunE = appRun(state, func(a *app.App) error {
		args := cmd.Flags().Args()
		a.ChatJSON(asJSON)
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Interactive: interactive, Session: session})
	})
	return cmd
}
