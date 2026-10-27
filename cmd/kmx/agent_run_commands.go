package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentRunCommand(state *commandState) *cobra.Command {
	var opt app.RunAgentOptions
	var file fileCommandOptions
	cmd := &cobra.Command{
		Use: "run [FILE [input]]", Short: "Preview an agent file or run an existing Orka Agent",
		Long: "Run FILE previews saved examples; FILE \"input\" runs only that input. Answers go to stdout; route/model to stderr.\n" +
			"File output is text/JSON; --case selects a partial preview. No native evidence or source edits.\n" +
			"File previews on fake, local and Copilot models use embedded Agent Sessions and retain instructions, inputs, answers, judge payloads and reasoning in cleartext SQLite journals under the kmx state directory (KMX_HOME when set), in local-preview/journals. Retention keeps the newest 20 runs at each start; active runs are kept and concurrent starts can leave more. Stop previews before removing journal files there to clear retained content. Copilot content checks stay provider-owned and run before journaling. Replay makes zero model calls. This is not native execution evidence.\n" +
			"--verbose prints content-free file execution diagnostics and timings on stderr.\n" +
			"Select a live Orka Agent with --agent (without FILE).\n" +
			"Task name, state and recovery instructions go to stderr; stdout contains only the answer.\n" +
			"A Task is never retried or deleted; exit code 2 means the wait expired and\n" +
			"the answer may not yet be readable. Use kmx cluster task result to retrieve it later.",
		Args: usageArgs(0, 0, "kmx run --agent <name> --prompt <text>"),
	}
	cmd.Flags().StringVar(&opt.Agent, "agent", "", "run this live Orka Agent (required without FILE)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "live Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().StringVar(&opt.Prompt, "prompt", "", "one-shot Task prompt")
	cmd.Flags().StringVar(&opt.PromptFile, "prompt-file", "", "read Task prompt from file, or - for stdin")
	cmd.Flags().DurationVar(&opt.Wait, "wait", 5*time.Minute, "time to wait for the answer (default 5m; 10s to 9m)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "", "loopback result-forward port (omitted: choose automatically)")
	registerFileCommandFlags(cmd, &file, true)
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 || len(args) == 2 {
			if err := validateFileCommand(cmd, state, file); err != nil {
				return fileUsageError(err)
			}
			if len(args) == 2 && (cmd.Flags().Changed("prompt") || cmd.Flags().Changed("prompt-file")) {
				return fileUsageError(fmt.Errorf("positional input cannot be combined with --prompt or --prompt-file"))
			}
			if cmd.Flags().Changed("prompt") && cmd.Flags().Changed("prompt-file") {
				return fileUsageError(fmt.Errorf("only one of --prompt or --prompt-file is allowed"))
			}
			return nil
		}
		if err := fileFlagsWithoutFile(cmd); err != nil {
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
		if len(args) == 1 || len(args) == 2 {
			if len(args) == 2 {
				opt.Prompt = args[1]
				file.PositionalInput = true
			}
			return runFileCommand(cmd, state, args[0], file, opt.Prompt, opt.PromptFile)
		}
		return liveRun(cmd, args)
	}
	return cmd
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
