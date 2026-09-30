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
	cmd := &cobra.Command{
		Use: "run [<bundle-dir>]", Short: "Run one Task against an existing Orka Agent",
		Long: "Run a bundle's deployed Agent, or select a live Agent with --agent.\n" +
			"Task name, state and recovery instructions go to stderr; stdout contains only the answer.\n" +
			"A Task is never retried or deleted; exit code 2 means the Task may still be running;\n" +
			"use kmx task result to retrieve it later. Other errors exit 1.",
		Args: usageArgs(0, 1, "kmx agent run <bundle-dir> --prompt <text> | kmx agent run --agent <name> --prompt <text>"),
	}
	cmd.Flags().StringVar(&opt.Agent, "agent", "", "run this live Orka Agent instead of a bundle")
	cmd.Flags().StringVar(&opt.ToContext, "to-context", "", "bundle destination (default: remembered lift target)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "live Agent namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().StringVar(&opt.Prompt, "prompt", "", "one-shot Task prompt")
	cmd.Flags().StringVar(&opt.PromptFile, "prompt-file", "", "read Task prompt from file, or - for stdin")
	cmd.Flags().DurationVar(&opt.Wait, "wait", 5*time.Minute, "time to wait for the answer (default 5m; 10s to 9m)")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := usageArgs(0, 1, "kmx agent run <bundle-dir> --prompt <text> | kmx agent run --agent <name> --prompt <text>")(cmd, args); err != nil {
			return err
		}
		if cmd.Flags().Changed("prompt") == cmd.Flags().Changed("prompt-file") {
			return fmt.Errorf("exactly one of --prompt or --prompt-file is required")
		}
		if cmd.Flags().Changed("prompt") && strings.TrimSpace(opt.Prompt) == "" {
			return fmt.Errorf("--prompt must not be empty")
		}
		if (len(args) == 1) == (strings.TrimSpace(opt.Agent) != "") {
			return fmt.Errorf("choose either a bundle directory or --agent")
		}
		if len(args) == 1 && (state.contextFlag != "" || cmd.Flags().Changed("namespace")) {
			return fmt.Errorf("bundle runs use --to-context, not --context or --namespace")
		}
		if opt.Agent != "" && cmd.Flags().Changed("to-context") {
			return fmt.Errorf("--to-context is only for bundle runs; use --context with --agent")
		}
		return nil
	}
	cmd.RunE = appRun(state, func(a *app.App) error {
		if len(cmd.Flags().Args()) > 0 {
			opt.BundleDir = cmd.Flags().Arg(0)
		}
		return a.RunAgent(opt)
	})
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
			"With --wait, wait for completion. State and recovery instructions go to stderr.\n" +
			"exit code 2 means the Task is not finished; try this command later. Failed or Cancelled exits 1.",
		Args: usageArgs(1, 1, "kmx task result <task> [--namespace <ns>] [--context <ctx>] [--wait]"),
	}
	cmd.Flags().StringVar(&opt.Namespace, "namespace", "", "Task namespace (default: "+app.OrkaNamespace+")")
	cmd.Flags().BoolVar(&opt.Follow, "wait", false, "wait up to 5m for an answer")
	cmd.Flags().StringVar(&opt.ResultPort, "result-port", "19180", "free loopback port for the temporary result forward")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Task = cmd.Flags().Arg(0)
		return a.TaskResult(opt)
	})
	return cmd
}
