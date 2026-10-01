package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Create agents; inspect and chat with Orka agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newAgentListCommand(state), newAgentShowCommand(state), newAgentCreateCommand(state), newAgentLiftCommand(state), newAgentStatusCommand(state), newAgentEvaluateCommand(state), newAgentRunCommand(state), newAgentChatCommand(state),
		retiredCommand("edit", "kubectl --context <ctx> -n <namespace> edit agents.core.orka.ai <name>; inspect with kmx agent show <name> --namespace <namespace>"))
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
	var output, namespace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the Orka Agents in a namespace",
		Long: `List Orka Agent resources.

These are the agents kmx agent create writes and kmx agent show inspects.

--namespace selects the namespace the Orka controller watches. It defaults to
the namespace the pinned installer uses, which is the same default kmx agent
chat resolves against.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json|yaml")
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace the Orka controller watches (default: "+app.OrkaNamespace+")")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json", "yaml"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.ListAgents(output, namespace) })
	return cmd
}

func newAgentCreateCommand(state *commandState) *cobra.Command {
	var opt app.CreateOptions
	cmd := &cobra.Command{Use: "create [name]", Short: "Create an Orka or Kagent declarative Agent", Long: `Create a declarative Orka Agent and its Provider as reviewable Kubernetes YAML.
Select explicitly the namespace the Orka controller watches. Provider type, model
identifier such as local/qwen2.5:3b, and a separately provisioned Secret are required.
This command does not build or deploy application images. Keep your Deployment
or chart; use kmx migrate for an existing application's model seam.
Interactive agent chat is Orka-only, and so is kmx agent list --namespace.
For azure-openai, --base-url is the HTTPS Azure resource root (not /openai/v1),
--azure-deployment is required and must equal --model: portable model.name names
the deployment Orka sends to the Azure SDK. --azure-api-version is optional;
if unset it is absent from generated YAML. The key file must contain only
its key, with no trailing newline; Orka does not trim it. In Bash or Zsh,
provision the existing Secret without printing the key:
kubectl --context <ctx> -n <ns> create secret generic <name> --from-file=api-key=<(tr -d '\r\n' < <path>)
Only pass --secret <name> to kmx; never pass the key in command arguments.

With --runtime kagent, create targets a preinstalled exact Kagent v0.10.2 and
requires a name and --description. --kagent-runtime selects go or python. Kagent tools use exactly
one server:tool1,tool2 binding; its Secret skeleton is review-only, and online
deployment creates ModelConfig then Agent without adoption or rollback. With
--task, Kagent stores the full task prompt, history, and answer in its database;
default upstream session retention is unlimited. Kubernetes audit policy may
also capture Service-proxy request and response bodies.

Offline output uses pinned v0.2.0 CRDs by default (main selects an immutable snapshot), not
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
	cmd.Flags().StringVar(&opt.Runtime, "runtime", "orka", "agent runtime: orka or kagent")
	cmd.Flags().StringVar(&opt.KagentRuntime, "kagent-runtime", "go", "Kagent declarative runtime: go or python")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "explicit target namespace; the selected runtime controller must watch it (required)")
	cmd.Flags().StringVar(&opt.Description, "description", "", "one-line description")
	cmd.Flags().StringVar(&opt.ProviderType, "provider-type", "", "Provider type: openai, anthropic or azure-openai (required)")
	cmd.Flags().StringVar(&opt.Model, "model", "", "Provider model identifier (required)")
	cmd.Flags().StringVar(&opt.Secret, "secret", "", "existing Provider Secret name (required)")
	cmd.Flags().StringVar(&opt.SecretKey, "secret-key", "api-key", "key name within the existing Secret; never a value")
	cmd.Flags().StringVar(&opt.BaseURL, "base-url", "", "HTTP(S) Provider endpoint (Azure requires an HTTPS resource root), no credentials/query/fragment")
	cmd.Flags().StringVar(&opt.AzureDeployment, "azure-deployment", "", "Azure OpenAI deployment name (required for azure-openai; must match --model)")
	cmd.Flags().StringVar(&opt.AzureAPIVersion, "azure-api-version", "", "Azure OpenAI API version (optional; omitted from YAML when unset)")
	cmd.Flags().StringVar(&opt.Instructions, "instructions", "", "file containing the system message")
	cmd.Flags().StringVar(&opt.Tools, "tools", "", "Orka: comma-separated names; Kagent: exactly one server:tool1,tool2 binding")
	cmd.Flags().StringVar(&opt.Skills, "skills", "", "comma-separated explicit Orka skill names")
	cmd.Flags().BoolVar(&opt.Coordination, "coordination", false, "enable agent-to-agent delegation (requires --allowed-agent)")
	cmd.Flags().StringArrayVar(&opt.AllowedAgents, "allowed-agent", nil, "Agent permitted as a delegation target in this namespace (repeat for each Agent)")
	cmd.Flags().StringVar(&opt.Task, "task", "", "first prompt: Orka Task or one Kagent A2A message; applying authorizes execution")
	cmd.Flags().StringVar(&opt.AgentRequestsPerMinute, "agent-requests-per-minute", "", "explicit positive Agent request limit (int32)")
	cmd.Flags().StringVar(&opt.AgentTokensPerMinute, "agent-tokens-per-minute", "", "explicit positive Agent token limit (int64)")
	cmd.Flags().StringVar(&opt.ProviderRequestsPerMinute, "provider-requests-per-minute", "", "explicit positive Provider request limit (int32)")
	cmd.Flags().StringVar(&opt.ProviderTokensPerMinute, "provider-tokens-per-minute", "", "explicit positive Provider token limit (int64)")
	cmd.Flags().StringVar(&opt.SchemaTarget, "schema-target", "", "offline only: v0.2.0 (default), v0.1.3, or pinned main")
	cmd.Flags().StringVar(&opt.ResultServiceAccount, "result-service-account", "", "existing ServiceAccount in the selected namespace for Task result access")
	cmd.Flags().StringVar(&opt.OrkaAPIService, "orka-api-service", "orka-api", "Orka API Service name exposing port 8080")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().StringVar(&opt.BundlePath, "bundle-path", "", "portable bundle directory (default agents/<name>; stdout requires this flag)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write the manifest and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation and local artifact; no cluster writes or execution")
	cmd.MarkFlagsMutuallyExclusive("no-apply", "dry-run")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"orka", "kagent"}))
	_ = cmd.RegisterFlagCompletionFunc("kagent-runtime", staticCompletion([]string{"go", "python"}))
	_ = cmd.RegisterFlagCompletionFunc("provider-type", staticCompletion([]string{"openai", "anthropic", "azure-openai"}))
	_ = cmd.RegisterFlagCompletionFunc("schema-target", staticCompletion([]string{"v0.2.0", "v0.1.3", "main"}))
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(opt.Runtime) != opt.Runtime {
			return fmt.Errorf("--runtime must be exactly orka or kagent")
		}
		if opt.Runtime != "orka" && opt.Runtime != "kagent" {
			return fmt.Errorf("unsupported agent runtime; use orka or kagent")
		}
		if opt.Runtime == "kagent" && len(args) != 1 {
			return fmt.Errorf("usage: kmx agent create <name> --runtime kagent [flags]")
		}
		if len(args) > 1 {
			return fmt.Errorf("usage: kmx agent create [<name>] [flags]")
		}
		if opt.Runtime != "kagent" && cmd.Flags().Changed("kagent-runtime") {
			return fmt.Errorf("--kagent-runtime requires --runtime kagent")
		}
		if opt.Runtime == "kagent" {
			if opt.KagentRuntime != "go" && opt.KagentRuntime != "python" {
				return fmt.Errorf("--kagent-runtime must be go or python")
			}
			for _, name := range []string{
				"skills", "coordination", "allowed-agent", "azure-deployment", "azure-api-version", "orka-api-service", "result-port", "agent-requests-per-minute",
				"agent-tokens-per-minute", "provider-requests-per-minute",
				"provider-tokens-per-minute", "schema-target", "result-service-account",
			} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s is unsupported or Orka-only with --runtime kagent", name)
				}
			}
		}
		return nil
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		if opt.Runtime == "orka" && len(cmd.Flags().Args()) == 0 {
			return a.CreateAgentInteractive(opt)
		}
		opt.Name = cmd.Flags().Arg(0)
		return a.CreateAgent(opt)
	})
	return cmd
}

func newAgentChatCommand(state *commandState) *cobra.Command {
	var interactive, verbose bool
	var runtime, namespace, azureDiscovery, bundles string
	cmd := &cobra.Command{Use: "chat <name> [message...]", Short: "Chat with an Orka Agent", Args: usageArgs(1, -1, "kmx agent chat --interactive [--namespace <namespace>] <name> [message]")}
	cmd.Flags().BoolVar(&interactive, "interactive", false, "open the Orka chat TUI (required: Orka chat is a session)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show chat WORKING and TIMING details")
	cmd.Flags().StringVar(&runtime, "runtime", "auto", "agent runtime: auto (detect the Orka Agent) or orka")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Orka Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().StringVar(&azureDiscovery, "azure-discovery", "cli", "AKS listing for /lift: cli or sdk")
	cmd.Flags().StringVar(&bundles, "bundles", "", "directory holding one agent bundle per agent for /lift (default: agents)")
	cmd.Flags().String("session", "", "retired server-side session flag")
	cmd.Flags().Bool("json", false, "retired raw A2A task flag")
	_ = cmd.Flags().MarkHidden("session")
	_ = cmd.Flags().MarkHidden("json")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"auto", "orka"}))
	_ = cmd.RegisterFlagCompletionFunc("azure-discovery", staticCompletion([]string{"cli", "sdk"}))
	cmd.ValidArgsFunction = completeLiveAgents
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("session") {
			return fmt.Errorf("--session is retired: Orka chat has no resumable server-side session; use kmx agent chat --interactive --namespace <ns> <name>")
		}
		if cmd.Flags().Changed("json") {
			return fmt.Errorf("--json is retired: there is no raw A2A JSON equivalent for an existing Agent; use kmx agent chat --interactive --namespace <ns> <name> for interactive chat")
		}
		return usageArgs(1, -1, "kmx agent chat --interactive [--namespace <namespace>] <name> [message]")(cmd, args)
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		args := cmd.Flags().Args()
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Interactive: interactive, Verbose: verbose, Runtime: runtime, Namespace: namespace, AzureDiscovery: azureDiscovery, Bundles: bundles})
	})
	return cmd
}
