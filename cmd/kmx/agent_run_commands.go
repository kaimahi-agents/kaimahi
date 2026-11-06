package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func newAgentRunCommand(state *commandState) *cobra.Command {
	var opt app.RunAgentOptions
	var file fileCommandOptions
	var once bool
	var upgrade, parallel, yes bool
	var runtimeName string
	cmd := &cobra.Command{
		Use: "run [FILE]", Short: "Run an agent file or an existing Orka Agent",
		Long: "Run FILE starts or reuses a resident agent and answers without checks.\n" +
			"--prompt sends only that input. Answers: stdout; diagnostics: stderr.\n" +
			"Use kmx test FILE for checks. Live --agent exit code 2 is pending; no retries.\n" +
			"Read pending Tasks: kmx cluster task result. File retention and privacy:\n" +
			previewCaveatsURL,
		Example: "  kmx run greeter.agent.md\n  kmx run greeter.agent.md --prompt \"Say hello\"\n  kmx run --agent greeter --prompt \"Say hello\"",
		Args:    usageArgs(0, 0, "kmx run --agent <name> --prompt <text>"),
	}
	cmd.Flags().StringVar(&opt.Agent, "agent", "", "Live Orka Agent (required without FILE)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().StringVarP(&opt.Prompt, "prompt", "p", "", "Input text (FILE or live Agent)")
	cmd.Flags().StringVar(&opt.PromptFile, "prompt-file", "", "Input file, or - for stdin (FILE/live Agent)")
	cmd.Flags().DurationVar(&opt.Wait, "wait", 5*time.Minute, "Wait for answer (10s to 9m)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "", "Loopback forward port (chosen automatically)")
	registerFileCommandFlags(cmd, &file, true)
	cmd.Flags().BoolVar(&once, "once", false, "Run FILE once without starting a resident agent")
	cmd.Flags().BoolVarP(&upgrade, "upgrade", "u", false, "Replace active generations after the new worker is ready")
	cmd.Flags().BoolVar(&parallel, "new", false, "Start a parallel resident generation")
	cmd.Flags().StringVar(&runtimeName, "name", "", "Runtime name for --new")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm --upgrade without prompting")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if upgrade && (parallel || once) {
			return fileUsageError(fmt.Errorf("--upgrade conflicts with --new and --once"))
		}
		if runtimeName != "" && !parallel {
			return fileUsageError(fmt.Errorf("--name requires --new"))
		}
		if yes && !upgrade {
			return fileUsageError(fmt.Errorf("--yes requires --upgrade"))
		}
		effectiveOnce := once || state.deps.goos == "windows" || !state.deps.residentAgents
		if len(args) == 2 {
			return fileUsageError(fmt.Errorf("run accepts one agent FILE; use --prompt TEXT to send input"))
		}
		if len(args) == 1 {
			if cmd.Flags().Changed("prompt") && (len(opt.Prompt) > agentfile.MaxInputBytes || !utf8.ValidString(opt.Prompt)) {
				return fileUsageError(fmt.Errorf("--prompt requires UTF-8 input of at most 16 KiB"))
			}
			if !effectiveOnce {
				if cmd.Flags().Changed("wait") && (opt.Wait < 10*time.Second || opt.Wait > 9*time.Minute) {
					return fileUsageError(fmt.Errorf("--wait must be between 10s and 9m"))
				}
				for _, name := range []string{"case", "output", "no-content", "verbose"} {
					if cmd.Flags().Changed(name) {
						return fileUsageError(fmt.Errorf("resident Run does not use --%s; add --once for file preview", name))
					}
				}
			}
			if err := validateFileCommand(cmd, state, file); err != nil {
				return fileUsageError(err)
			}
			if cmd.Flags().Changed("prompt") && cmd.Flags().Changed("prompt-file") {
				return fileUsageError(fmt.Errorf("only one of --prompt or --prompt-file is allowed"))
			}
			return nil
		}
		if once {
			return fileUsageError(fmt.Errorf("--once requires an explicit agent FILE"))
		}
		if upgrade || parallel || runtimeName != "" || yes {
			return fileUsageError(fmt.Errorf("resident lifecycle flags require an explicit agent FILE"))
		}
		if err := fileFlagsWithoutFile(cmd); err != nil {
			if cmd.Flags().Changed("tests") || cmd.Flags().Changed("case") {
				return fileUsageError(err)
			}
			return err
		}
		if cmd.Flags().Changed("output") {
			return fmt.Errorf("--output requires an explicit agent FILE")
		}
		if err := usageArgs(0, 0, "kmx run FILE or kmx run --agent <name> --prompt <text>")(cmd, args); err != nil {
			return err
		}
		if cmd.Flags().Changed("wait") && opt.Wait == 0 {
			return fmt.Errorf("--wait must be between 10s and 9m")
		}
		if cmd.Flags().Changed("prompt") == cmd.Flags().Changed("prompt-file") {
			return fmt.Errorf("exactly one of --prompt or --prompt-file is required")
		}
		if cmd.Flags().Changed("prompt") && strings.TrimSpace(opt.Prompt) == "" {
			return fmt.Errorf("--prompt must not be empty")
		}
		if strings.TrimSpace(opt.Agent) == "" {
			return fmt.Errorf("--agent is required")
		}
		return nil
	}
	liveRun := appRun(state, func(a *app.App) error { return a.RunAgent(opt) })
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		effectiveOnce := once || state.deps.goos == "windows" || !state.deps.residentAgents
		if len(args) == 1 {
			file.InputOnly = cmd.Flags().Changed("prompt") || cmd.Flags().Changed("prompt-file")
			if !effectiveOnce {
				if cmd.Flags().Changed("prompt-file") {
					input, err := readPromptInput(state, opt.PromptFile)
					if err != nil {
						return err
					}
					opt.Prompt = input
				}
				if file.ToolBindings != "" || len(file.MCPServers) > 0 || file.NoMCPServers {
					return fileUsageError(fmt.Errorf("resident local agents currently refuse explicit tool or MCP overrides"))
				}
				pair, err := agentfile.LoadPair(args[0], file.TestsFile)
				if err != nil {
					return err
				}
				provider := file.Provider
				if provider == "" {
					provider = "copilot"
					if file.Model == "fake" || file.Model == "" && pair.Agent.Model == "fake" {
						provider = "fake"
					}
				}
				model := file.Model
				if model == "" {
					model = pair.Agent.Model
				}
				if model == "" && provider == "copilot" {
					model = modelaccess.CopilotDefaultModel
				}
				route := fileModelRoute(provider, file.Endpoint)
				route.Model = model
				if file.Endpoint != "" || provider != "copilot" && provider != "fake" || !agentfile.ValidModel(model) || secretshapes.Match(model) != nil || route.Validate() != nil || (provider == "fake") != (model == "fake") || len(pair.Agent.Tools) != 0 {
					return fileUsageError(fmt.Errorf("resident local agents currently require a tool-free fake or Copilot route without a custom endpoint"))
				}
				mode := app.LocalAgentActivationNormal
				if upgrade {
					mode = app.LocalAgentActivationUpgrade
				}
				if parallel {
					mode = app.LocalAgentActivationNew
					if runtimeName == "" {
						var nameErr error
						runtimeName, nameErr = promptLocalAgentName(state.deps.stdin, cmd.ErrOrStderr(), pair.Agent.Identity.ID)
						if nameErr != nil {
							return nameErr
						}
					}
				}
				activation := app.LocalAgentActivationOptions{Mode: mode, Name: runtimeName}
				if upgrade && !yes {
					rows, readErr := state.deps.readLocalAgents(false)
					if readErr != nil {
						return readErr
					}
					sessions := 0
					for _, row := range rows {
						if row.ID == app.LocalAgentID(pair.AgentPath) {
							sessions += row.Sessions
						}
					}
					confirmed, confirmErr := confirmLocalAgentUpgrade(state.deps.stdin, cmd.ErrOrStderr(), sessions)
					if confirmErr != nil {
						return confirmErr
					}
					if !confirmed {
						return fmt.Errorf("local agent activation cancelled")
					}
				}
				instance, created, err := state.deps.activateLocalAgent(cmd.Context(), state.executable(), pair, provider, file.Endpoint, model, activation)
				var drift *app.LocalAgentDriftError
				if errors.As(err, &drift) && !upgrade && !parallel {
					choice, name, promptErr := promptLocalAgentDrift(state.deps.stdin, cmd.ErrOrStderr(), drift.Active)
					if promptErr != nil {
						return promptErr
					}
					if choice == app.LocalAgentActivationNormal {
						return fmt.Errorf("local agent activation cancelled")
					}
					activation.Mode, activation.Name = choice, name
					instance, created, err = state.deps.activateLocalAgent(cmd.Context(), state.executable(), pair, provider, file.Endpoint, model, activation)
				}
				if err != nil {
					return err
				}
				verb := "Reused"
				if created {
					verb = "Started"
				}
				if instance.LastError != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", instance.LastError)
				}
				if len(args) == 1 && !cmd.Flags().Changed("prompt") && !cmd.Flags().Changed("prompt-file") {
					_, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s %s (%s) · snapshot %s\n", verb, instance.Name, instance.ID, instance.SnapshotRevision)
					return err
				}
				session, err := app.OpenLocalAgentSession(cmd.Context(), instance.InstanceID, "run")
				if err != nil {
					return err
				}
				defer app.CloseLocalAgentSession(context.Background(), instance.InstanceID, session.ID)
				input := opt.Prompt
				invokeCtx := cmd.Context()
				var cancel context.CancelFunc
				if opt.Wait > 0 {
					invokeCtx, cancel = context.WithTimeout(invokeCtx, opt.Wait)
					defer cancel()
				}
				answer, err := app.InvokeLocalAgent(invokeCtx, instance.InstanceID, session.ID, input)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), answer); err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.ErrOrStderr(), "%s %s (%s) · session %s\n", verb, instance.Name, instance.ID, session.ID)
				return err
			}
			return runFileCommand(cmd, state, args[0], file, opt.Prompt, opt.PromptFile)
		}
		return liveRun(cmd, args)
	}
	return cmd
}

func promptLocalAgentDrift(in *os.File, out io.Writer, active app.LocalAgentInstance) (app.LocalAgentActivationMode, string, error) {
	diagnostics, ok := out.(*os.File)
	if in == nil || !ok || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(diagnostics.Fd())) {
		return "", "", fmt.Errorf("local agent snapshot changed; nonterminal use requires --upgrade --yes or --new --name NAME")
	}
	scanner := bufio.NewScanner(createPromptReader{in})
	fmt.Fprint(out, "[u] Upgrade  [n] Start new  [c] Cancel [c]: ")
	if !scanner.Scan() {
		return "", "", fmt.Errorf("read lifecycle choice: %w", scanner.Err())
	}
	switch localAgentDriftChoice(scanner.Text()) {
	case "u":
		confirmed, err := confirmLocalAgentUpgradeScanner(scanner, out, active.Sessions)
		if err != nil {
			return "", "", err
		}
		if !confirmed {
			return app.LocalAgentActivationNormal, "", nil
		}
		return app.LocalAgentActivationUpgrade, "", nil
	case "n":
		def := active.AuthoredName + "-<instance>"
		fmt.Fprintf(out, "Runtime name [%s]: ", def)
		if !scanner.Scan() {
			return "", "", fmt.Errorf("read runtime name: %w", scanner.Err())
		}
		name := strings.TrimSpace(scanner.Text())
		return app.LocalAgentActivationNew, name, nil
	default:
		return app.LocalAgentActivationNormal, "", nil
	}
}

func localAgentDriftChoice(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "u":
		return "u"
	case "n":
		return "n"
	default:
		return "c"
	}
}

func confirmLocalAgentUpgrade(in *os.File, out io.Writer, sessions int) (bool, error) {
	diagnostics, ok := out.(*os.File)
	if in == nil || !ok || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(diagnostics.Fd())) {
		return false, fmt.Errorf("--upgrade requires confirmation in a terminal; add --yes for noninteractive use")
	}
	return confirmLocalAgentUpgradeScanner(bufio.NewScanner(createPromptReader{in}), out, sessions)
}

func promptLocalAgentName(in *os.File, out io.Writer, authored string) (string, error) {
	diagnostics, ok := out.(*os.File)
	if in == nil || !ok || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(diagnostics.Fd())) {
		return "", fmt.Errorf("nonterminal --new requires --name NAME")
	}
	fmt.Fprintf(out, "Runtime name [%s-<instance>]: ", authored)
	scanner := bufio.NewScanner(createPromptReader{in})
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(scanner.Text()), nil
}

func confirmLocalAgentUpgradeScanner(scanner *bufio.Scanner, out io.Writer, sessions int) (bool, error) {
	fmt.Fprintf(out, "Upgrade will close %d active sessions. Continue? [y/N]: ", sessions)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y", nil
}

func newTaskCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "task", Short: "Inspect Orka Tasks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	group.AddCommand(newTaskResultCommand(state))
	return group
}

func newTaskResultCommand(state *commandState) *cobra.Command {
	var opt app.TaskResultOptions
	cmd := &cobra.Command{
		Use: "result <task>", Short: "Read an existing AI Task's phase and answer",
		Long: "Read an AI Task's phase. If its answer is available, print only the answer on stdout.\n" +
			"With --wait <duration> (10s to 9m), wait for completion. Phase changes and recovery instructions go to stderr.\n" +
			"exit code 2 means the Task is pending or its successful answer is not yet readable; retry later. Failed or Cancelled exits 1.",
		Args: usageArgs(1, 1, "kmx cluster task result <task> [--context <ctx>] [--namespace <ns>] [--wait 5m]"),
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "Task namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().DurationVar(&opt.Wait, "wait", 0, "wait up to this duration for an answer (10s to 9m; omitted: inspect once)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "", "loopback result-forward port (omitted: choose automatically)")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := usageArgs(1, 1, "kmx cluster task result <task> [--namespace <ns>] [--context <ctx>] [--wait 5m]")(cmd, args); err != nil {
			return err
		}
		if cmd.Flags().Changed("wait") && opt.Wait == 0 {
			return fmt.Errorf("--wait must be between 10s and 9m")
		}
		return nil
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Task = cmd.Flags().Arg(0)
		return a.TaskResult(opt)
	})
	return cmd
}
