package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newBundleCommand(state *commandState) *cobra.Command {
	// Hidden: legacy agent.yaml bundles are removed once digest lift and the
	// quickstart/console callers reach suite-path parity.
	group := &cobra.Command{Use: "bundle", Short: "Legacy agent.yaml bundles (being removed)", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newAgentLiftCommand(state), newAgentRetireCommand(state), newAgentStatusCommand(state), newAgentEvaluateCommand(state), newAgentVerifyCommand())
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

These are the live agents kmx create --live writes and kmx orka show inspects.

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
	var opt app.CreateOptions
	var workspaceRoot, workspaceVersion string
	var workspaceRuntime workspace.RuntimeProfile
	var live bool
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Author an offline workspace member, or create live Orka or kagent agents",
		Long: `Create one native workspace member offline by default.
Orka is the default runtime; --runtime kagent authors exact v0.10.3 Go
private metadata. --runtime-version may explicitly select v0.10.3 for kagent.
--runtime agentsessions authors the pinned built-in chat profile offline only;
its optional --runtime-version must equal ` + workspace.AgentSessionsVersion + `.
AgentSessions evaluation, lift, chat and status remain unavailable.

A name, exact --model and file-valued --instructions are required; --description
is optional. Discover the nearest ancestor workspace.yaml. An existing workspace
has a one-member limit: use a new --workspace directory for another member.
Without a workspace, a visible terminal can confirm initialization in the current
directory. Scripts must pass --workspace <directory> to initialize explicitly.
No cluster settings are loaded, no image is built and no Agent is deployed.
Workspace evaluation and release lift are not yet available.

Use --live for direct Orka or kagent resources, including bundle/YAML previews.
It bypasses workspace discovery and conflicts with --workspace. All live-only
flags require --live; --runtime kagent never selects live mode implicitly.
Live named creation applies to the selected cluster by default. Use --no-apply to
validate and write local artifacts without applying, or --dry-run for server
validation and local artifacts. --out - writes YAML to stdout without applying.
With --out -, explicitly set --bundle-path to also write an authored bundle.
With --live, omit the name for guided Orka creation in a terminal; kagent creation requires a name.
Select explicitly the namespace the runtime controller watches. Provider type, model
identifier and a separately provisioned Secret are required.
This command does not build or deploy application images. Keep your Deployment
or chart. Interactive kmx chat and kmx orka list are Orka-only.

Orka creation:
The generated resources include a Provider and Agent. Model identifiers include
local/qwen2.5:3b.
For azure-openai, --base-url is the HTTPS Azure resource root (not /openai/v1),
--azure-deployment is required and must equal --model: portable model.name names
the deployment Orka sends to the Azure SDK. --azure-api-version is optional;
if unset it is absent from generated YAML. The key file must contain only
its key, with no trailing newline; Orka does not trim it. In Bash or Zsh,
provision the existing Secret without printing the key:
kubectl --context <ctx> -n <ns> create secret generic <name> --from-file=api-key=<(tr -d '\r\n' < <path>)
Only pass --secret <name> to kmx; never pass the key in command arguments.

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
do not bind returned result bytes to a UID. Dry-run tests neither access nor execution.

kagent creation:
With --live --runtime kagent, create targets a preinstalled exact kagent v0.10.2 and
requires a name and --description. --kagent-runtime selects go or python. kagent tools use exactly
one server:tool1,tool2 binding; its Secret skeleton is review-only, and online
deployment creates ModelConfig then Agent without adoption or rollback. With
--task, kagent stores the full task prompt, history, and answer in its database;
default upstream session retention is unlimited. Kubernetes audit policy may
also capture Service-proxy request and response bodies.`,
		Example: `  # Offline workspace member; explicit initialization needs no terminal
  kmx create greeter --workspace ./greeter --model qwen-3.5-2b --instructions instructions.md

  # Guided live Orka creation (terminal required)
  kmx create --live

  # Offline YAML preview; no cluster access
  kmx create --live preview --namespace orka-system \
    --provider-type openai --model qwen2.5:3b --secret local-provider-key \
    --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --out -

  # Also persist the authored bundle
  kmx create --live preview --namespace orka-system \
    --provider-type openai --model qwen2.5:3b --secret local-provider-key \
    --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --out - --bundle-path agents/preview`,
	}
	cmd.Flags().BoolVar(&live, "live", false, "create live Orka or kagent resources or legacy bundle/YAML previews")
	cmd.Flags().StringVar(&workspaceRoot, "workspace", "", "select an exact workspace directory (initialize without prompting if absent)")
	_ = cmd.MarkFlagDirname("workspace")
	cmd.Flags().StringVar(&opt.Runtime, "runtime", "orka", "agent runtime: orka, kagent, or offline-only agentsessions")
	cmd.Flags().StringVar(&opt.KagentRuntime, "kagent-runtime", "go", "kagent declarative runtime (workspace: Go only; live create: go or python)")
	cmd.Flags().StringVar(&workspaceVersion, "runtime-version", "v0.10.3", "workspace-only exact runtime profile (kagent: v0.10.3; agentsessions: pinned commit)")
	_ = cmd.RegisterFlagCompletionFunc("runtime-version", staticCompletion([]string{"v0.10.3", workspace.AgentSessionsVersion}))
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
	cmd.Flags().StringVar(&opt.Tools, "tools", "", "Orka: comma-separated names; kagent: exactly one server:tool1,tool2 binding")
	cmd.Flags().StringVar(&opt.Skills, "skills", "", "comma-separated explicit Orka skill names")
	cmd.Flags().BoolVar(&opt.Coordination, "coordination", false, "enable agent-to-agent delegation (requires --allowed-agent)")
	cmd.Flags().StringArrayVar(&opt.AllowedAgents, "allowed-agent", nil, "Agent permitted as a delegation target in this namespace (repeat for each Agent)")
	cmd.Flags().StringVar(&opt.Task, "task", "", "first prompt: Orka Task or one kagent A2A message; applying authorizes execution")
	cmd.Flags().StringVar(&opt.AgentRequestsPerMinute, "agent-requests-per-minute", "", "explicit positive Agent request limit (int32)")
	cmd.Flags().StringVar(&opt.AgentTokensPerMinute, "agent-tokens-per-minute", "", "explicit positive Agent token limit (int64)")
	cmd.Flags().StringVar(&opt.ProviderRequestsPerMinute, "provider-requests-per-minute", "", "explicit positive Provider request limit (int32)")
	cmd.Flags().StringVar(&opt.ProviderTokensPerMinute, "provider-tokens-per-minute", "", "explicit positive Provider token limit (int64)")
	cmd.Flags().StringVar(&opt.SchemaTarget, "schema-target", "", "offline only: v0.2.0 (default), v0.1.3, or pinned main")
	cmd.Flags().StringVar(&opt.ResultServiceAccount, "result-service-account", "", "existing ServiceAccount in the selected namespace for Task result access")
	cmd.Flags().StringVar(&opt.OrkaAPIService, "orka-api-service", "orka-api", "Orka API Service name exposing port 8080")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().StringVar(&opt.BundlePath, "bundle-path", "", "authored bundle directory (default agents/<name>; with --out -, set explicitly to also write a bundle)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write the manifest and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation and local artifact; no cluster writes or execution")
	cmd.MarkFlagsMutuallyExclusive("no-apply", "dry-run")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"orka", "kagent", "agentsessions"}))
	_ = cmd.RegisterFlagCompletionFunc("kagent-runtime", staticCompletion([]string{"go", "python"}))
	_ = cmd.RegisterFlagCompletionFunc("provider-type", staticCompletion([]string{"openai", "anthropic", "azure-openai"}))
	_ = cmd.RegisterFlagCompletionFunc("schema-target", staticCompletion([]string{"v0.2.0", "v0.1.3", "main"}))
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("workspace") && strings.TrimSpace(workspaceRoot) == "" {
			return fmt.Errorf("workspace creation requires a non-empty --workspace directory")
		}
		if live && cmd.Flags().Changed("workspace") {
			return fmt.Errorf("--live cannot be used with --workspace")
		}
		if !live {
			if err := validateWorkspaceCreateFlags(cmd, state); err != nil {
				return err
			}
			if len(args) != 1 {
				return fmt.Errorf("workspace creation requires a name: kmx create <name> --model <model> --instructions <file>; use kmx create --live for guided Orka creation")
			}
			workspaceRuntime = workspace.RuntimeProfile{ID: opt.Runtime}
			if opt.Runtime == "kagent" {
				if workspaceVersion != "v0.10.3" || opt.KagentRuntime != "go" {
					return fmt.Errorf("workspace kagent supports only --runtime-version v0.10.3 and --kagent-runtime go")
				}
				workspaceRuntime.Version, workspaceRuntime.Engine = workspaceVersion, opt.KagentRuntime
			} else if opt.Runtime == "agentsessions" {
				if (cmd.Flags().Changed("runtime-version") && workspaceVersion != workspace.AgentSessionsVersion) || cmd.Flags().Changed("kagent-runtime") {
					return fmt.Errorf("workspace agentsessions requires the pinned --runtime-version %s and refuses --kagent-runtime", workspace.AgentSessionsVersion)
				}
				workspaceRuntime.Version, workspaceRuntime.Engine = workspace.AgentSessionsVersion, "chat"
			} else if opt.Runtime != "orka" || cmd.Flags().Changed("runtime-version") || cmd.Flags().Changed("kagent-runtime") {
				return fmt.Errorf("workspace --runtime must be orka without version/engine or exact kagent v0.10.3 go")
			}
			if strings.TrimSpace(opt.Model) == "" || strings.TrimSpace(opt.Instructions) == "" {
				return fmt.Errorf("workspace creation requires --model and --instructions <file>")
			}
			return nil
		}
		if cmd.Flags().Changed("runtime-version") {
			return fmt.Errorf("--runtime-version is only for offline authoring; live kagent create remains v0.10.2")
		}
		if strings.TrimSpace(opt.Runtime) != opt.Runtime {
			return fmt.Errorf("--runtime must be exactly orka or kagent")
		}
		if opt.Runtime != "orka" && opt.Runtime != "kagent" {
			return fmt.Errorf("unsupported agent runtime; use orka or kagent")
		}
		if opt.Runtime == "kagent" && len(args) != 1 {
			return fmt.Errorf("usage: kmx create --live <name> --runtime kagent [flags]")
		}
		if len(args) > 1 {
			return fmt.Errorf("usage: kmx create --live [<name>] [flags]")
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
	runNative := appRun(state, func(a *app.App) error {
		if opt.Runtime == "orka" && len(cmd.Flags().Args()) == 0 {
			return a.CreateAgentInteractive(opt)
		}
		opt.Name = cmd.Flags().Arg(0)
		return a.CreateAgent(opt)
	})
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !live {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Stdin: state.deps.stdin}
			return a.CreateSuiteMember(cmd.Context(), workspaceRoot, workspace.CreateOptions{
				Name: args[0], Description: opt.Description, Model: opt.Model, InstructionsFile: opt.Instructions, Runtime: workspaceRuntime,
			})
		}
		return runNative(cmd, args)
	}
	return cmd
}

func newAgentChatCommand(state *commandState) *cobra.Command {
	var verbose bool
	var runtime, namespace, azureDiscovery, bundles string
	usage := "kmx chat [--namespace <namespace>] <name> [message]"
	cmd := &cobra.Command{Use: "chat <name> [message...]", Short: "Chat with an Orka Agent", Long: "Open an interactive chat session with an Orka Agent. An optional message is sent as the first turn; for a one-shot Task use kmx run.", Args: usageArgs(1, -1, usage)}
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
			return fmt.Errorf("--session is retired: Orka chat has no resumable server-side session; use kmx chat --namespace <ns> <name>")
		}
		if cmd.Flags().Changed("json") {
			return fmt.Errorf("--json is retired: there is no raw A2A JSON equivalent for an existing Agent; use kmx chat --namespace <ns> <name> for interactive chat")
		}
		return usageArgs(1, -1, usage)(cmd, args)
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		args := cmd.Flags().Args()
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Verbose: verbose, Runtime: runtime, Namespace: namespace, AzureDiscovery: azureDiscovery, Bundles: bundles})
	})
	return cmd
}
