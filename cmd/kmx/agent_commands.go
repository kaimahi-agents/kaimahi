package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
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
		Args:  usageArgs(1, 1, "kmx cluster orka show <name> --namespace <namespace>"),
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

These are live Agent resources; kmx cluster orka show inspects their dependencies.

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
	var root, name, description, model, instructions, runtimeID, version, engine, members, flow string
	var profile workspace.RuntimeProfile
	cmd := &cobra.Command{
		Use: "create <name>", Short: "Author an offline workspace member",
		Long: "Create a native workspace member, not an agent markdown file.\n" +
			"Scripts require --model and --instructions; terminals can prompt.\n" +
			"Nothing is evaluated, published or deployed.\n" +
			"Runtime profiles and experimental flow authoring:\n" +
			"https://github.com/kaimahi-agents/kaimahi/blob/main/docs/suite-workspace.md",
		Example: "  kmx package create greeter\n  kmx package create greeter --workspace . --model qwen --instructions prompt.md\n  kmx package create team --workspace . --members members.json --flow flow.json",
	}
	cmd.Flags().StringVar(&root, "workspace", "", "Workspace directory; skips initialization prompt")
	_ = cmd.MarkFlagDirname("workspace")
	cmd.Flags().StringVar(&members, "members", "", "Experimental members JSON; needs --flow/--workspace")
	cmd.Flags().StringVar(&flow, "flow", "", "Experimental flow JSON; needs --members")
	_ = cmd.MarkFlagFilename("members", "json")
	_ = cmd.MarkFlagFilename("flow", "json")
	cmd.Flags().StringVar(&description, "description", "", "one-line description")
	cmd.Flags().StringVar(&model, "model", "", "Exact model (prompted in a terminal)")
	cmd.Flags().StringVar(&instructions, "instructions", "", "System-message file (prompted in a terminal)")
	cmd.Flags().StringVar(&runtimeID, "runtime", "orka", "orka, kagent or agentsessions")
	cmd.Flags().StringVar(&version, "runtime-version", "v0.10.3", "Exact profile (kagent: v0.10.3)")
	cmd.Flags().StringVar(&engine, "kagent-runtime", "go", "kagent engine: go")
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
			return fmt.Errorf("workspace creation requires a name: kmx package create <name> --model <model> --instructions <file>")
		}
		name = args[0]
		if cmd.Flags().Changed("members") || cmd.Flags().Changed("flow") {
			for _, flag := range []string{"model", "instructions", "description", "runtime", "runtime-version", "kagent-runtime"} {
				if cmd.Flags().Changed(flag) {
					return fmt.Errorf("--%s cannot be used with experimental flow creation", flag)
				}
			}
			if strings.TrimSpace(root) == "" || strings.TrimSpace(members) == "" || strings.TrimSpace(flow) == "" {
				return fmt.Errorf("experimental flow creation requires --workspace DIR --members FILE --flow FILE")
			}
			return nil
		}
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
		if cmd.Flags().Changed("model") && strings.TrimSpace(model) == "" || cmd.Flags().Changed("instructions") && strings.TrimSpace(instructions) == "" {
			return fmt.Errorf("workspace creation requires --model and --instructions <file>")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("members") || cmd.Flags().Changed("flow") {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Stdin: state.deps.stdin}
			return a.CreateSuiteFlow(cmd.Context(), root, workspace.FlowCreateOptions{Name: name, MembersFile: members, FlowFile: flow})
		}
		opt := workspace.CreateOptions{Name: name, Description: description, Model: model, InstructionsFile: instructions, Runtime: profile}
		if err := completeCreateInputs(cmd.Context(), state.deps.stdin, cmd.ErrOrStderr(), &opt); err != nil {
			return err
		}
		a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Stdin: state.deps.stdin}
		return a.CreateSuiteMember(cmd.Context(), root, opt)
	}
	return cmd
}

func completeCreateInputs(ctx context.Context, in *os.File, out io.Writer, opt *workspace.CreateOptions) error {
	if strings.TrimSpace(opt.Model) != "" && strings.TrimSpace(opt.InstructionsFile) != "" {
		return nil
	}
	diagnostics, visible := out.(*os.File)
	if in == nil || !visible || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(diagnostics.Fd())) {
		return fmt.Errorf("workspace creation requires --model and --instructions <file> without a visible terminal")
	}
	// Name diagnostics can quote input; preserve the core's credential check
	// before issuing them, even though source inputs are not yet complete.
	if secretshapes.Match(opt.Name) != nil {
		return fmt.Errorf("refusing credential-shaped workspace name")
	}
	if err := scaffold.ValidateName(opt.Name); err != nil {
		return err
	}
	scanner := bufio.NewScanner(createPromptReader{in})
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && bytes.IndexByte(data, '\n') < 0 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return bufio.ScanLines(data, atEOF)
	})
	for _, field := range []struct {
		label string
		value *string
	}{{"Exact model", &opt.Model}, {"Instructions file", &opt.InstructionsFile}} {
		if strings.TrimSpace(*field.value) != "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "%s: ", field.label); err != nil {
			return err
		}
		read := scanner.Scan()
		if err := ctx.Err(); err != nil {
			return err
		}
		if !read {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			return fmt.Errorf("read %s: %w", field.label, err)
		}
		if strings.TrimSpace(scanner.Text()) == "" {
			return fmt.Errorf("%s is required; no files created", field.label)
		}
		*field.value = scanner.Text()
	}
	return nil
}

// Read no further than the current line: the core workspace confirmation uses
// its own scanner, so buffered answers must remain on stdin for that prompt.
type createPromptReader struct{ io.Reader }

func (r createPromptReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.Reader.Read(p[:1])
}

func newAgentChatCommand(state *commandState) *cobra.Command {
	var verbose bool
	var runtime, namespace, session, label string
	var fresh bool
	usage := "kmx chat [--namespace <namespace>] <name> [message]"
	cmd := &cobra.Command{Use: "chat <name> [message...]", Short: "Chat with a resident local or live Orka Agent",
		Long: "Open a session with a resident local or existing live Orka Agent.\n" +
			"An optional message starts the first turn. Local sessions can be resumed.\n" +
			"For a one-shot Task, use kmx run --agent.",
		Example: "  kmx chat greeter\n  kmx chat greeter \"Explain your last result\"",
		Args:    usageArgs(1, -1, usage)}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show chat WORKING and TIMING details")
	cmd.Flags().StringVar(&runtime, "runtime", "auto", "agent runtime: auto, local or orka")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Orka Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().StringVar(&session, "session", "", "resume a local session ID")
	cmd.Flags().BoolVar(&fresh, "new", false, "create a new local session")
	cmd.Flags().StringVar(&label, "label", "", "label a new local session")
	cmd.Flags().Bool("json", false, "retired raw A2A task flag")
	_ = cmd.Flags().MarkHidden("json")
	_ = cmd.RegisterFlagCompletionFunc("runtime", staticCompletion([]string{"auto", "local", "orka"}))
	cmd.ValidArgsFunction = completeLiveAgents
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if runtime != "auto" && runtime != "local" && runtime != "orka" {
			return fmt.Errorf("--runtime must be auto, local or orka")
		}
		if fresh && session != "" {
			return fmt.Errorf("--new and --session are mutually exclusive")
		}
		if runtime == "orka" && (fresh || session != "" || label != "") {
			return fmt.Errorf("local session flags require --runtime local or auto")
		}
		if cmd.Flags().Changed("json") {
			return fmt.Errorf("--json is retired: there is no raw A2A JSON equivalent for an existing Agent; use kmx chat --namespace <ns> <name> for interactive chat")
		}
		return usageArgs(1, -1, usage)(cmd, args)
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		args := cmd.Flags().Args()
		localIntent := runtime == "local" || fresh || session != "" || label != ""
		orkaIntent := runtime == "orka" || state.contextFlag != "" || cmd.Flags().Changed("namespace")
		if localIntent && orkaIntent {
			return fmt.Errorf("local session flags cannot be combined with Orka context or namespace selectors")
		}
		if state.deps.residentAgents && runtime != "orka" && state.contextFlag == "" && !cmd.Flags().Changed("namespace") {
			local, localErr := state.deps.readLocalAgent(args[0])
			if localErr != nil && runtime == "auto" && !localIntent {
				if active, err := state.deps.readLocalAgents(false); err == nil {
					matches := 0
					for _, row := range active {
						match := row.Name == args[0] || row.InstanceID == args[0] || row.ID == args[0]
						if len(args[0]) >= 8 && (strings.HasPrefix(strings.TrimPrefix(row.InstanceID, "li-"), args[0]) || strings.HasPrefix(strings.TrimPrefix(row.ID, "md-"), args[0])) {
							match = true
						}
						if match {
							matches++
						}
					}
					if matches == 0 {
						localErr = os.ErrNotExist
					}
					if matches > 1 {
						return fmt.Errorf("local agent selector is ambiguous; use a runtime name or instance ID")
					}
				}
			}
			if localErr == nil && local.State == "running" {
				if session == "" {
					opened, err := app.SelectLocalAgentSession(cmd.Context(), local.InstanceID, label, fresh)
					if err != nil {
						return err
					}
					session = opened.ID
					if fresh {
						fmt.Fprintln(cmd.ErrOrStderr(), "Session:", session)
					}
				}
				message := joinArgs(args[1:])
				if message != "" {
					answer, err := app.InvokeLocalAgent(cmd.Context(), local.InstanceID, session, message)
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(cmd.OutOrStdout(), answer)
					return err
				}
				return app.ChatLocalAgent(cmd.Context(), local.InstanceID, session, state.deps.stdin, cmd.OutOrStdout())
			}
			if localErr != nil && !errors.Is(localErr, os.ErrNotExist) {
				return localErr
			}
			if localErr == nil && local.State != "stopped" || localIntent {
				return fmt.Errorf("local agent is not running")
			}
		}
		a, err := state.operationApplication(cmd)
		if err != nil {
			return err
		}
		return a.ChatWithOptions(app.ChatOptions{Agent: args[0], Task: joinArgs(args[1:]), Verbose: verbose, Runtime: runtime, Namespace: namespace})
	}
	return cmd
}
