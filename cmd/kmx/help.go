package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const previewCaveatsURL = "https://github.com/kaimahi-agents/kaimahi/blob/main/docs/local-preview.md"

func configureCommandHelp(root *cobra.Command, state *commandState) {
	// Cobra's sorting switch is global. Order only this root's rendered help,
	// and preserve the default renderer for operational and legacy children.
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != root {
			defaultHelp(cmd, args)
			return
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%s\n\nUsage:\n  %s [command]\n\nAvailable Commands:\n", cmd.Short, cmd.Name())
		for _, name := range []string{"create", "run", "test", "chat", "model", "package", "cluster", "version"} {
			child, _, _ := cmd.Find([]string{name})
			fmt.Fprintf(out, "  %-10s %s\n", child.Name(), child.Short)
		}
		fmt.Fprintf(out, "\nFlags:\n%s\nUse %q for more information.\n", cmd.LocalFlags().FlagUsagesWrapped(80), cmd.Name()+" help [command]")
	})
	for _, child := range root.Commands() {
		switch child.Name() {
		case "mcp", "completion":
			child.Hidden = true
		case "create", "run", "test", "chat", "model":
			child.SetHelpFunc(func(cmd *cobra.Command, args []string) {
				if cmd == child {
					shortCommandHelp(cmd, args)
					return
				}
				defaultHelp(cmd, args)
			})
		}
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return flagErrorWithHelp(cmd, state, err)
	})
}

func flagErrorWithHelp(cmd *cobra.Command, state *commandState, err error) error {
	// Never replay argv or flag values: only the executable and resolved topic.
	topic := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name())
	if state.deps.goos == "windows" {
		// PowerShell treats straight and curly apostrophes as string delimiters.
		quotes := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
		return fmt.Errorf("%w\nPowerShell: & '%s'%s --help", err, quotes.Replace(state.executable()), topic)
	}
	return fmt.Errorf("%w\nRun %s%s --help for available flags", err, quoteShell(state.executable()), topic)
}

func shortCommandHelp(cmd *cobra.Command, _ []string) {
	out := cmd.OutOrStdout()
	description := cmd.Long
	if description == "" {
		description = cmd.Short
	}
	fmt.Fprintf(out, "%s\n\nUsage:\n  %s\n", strings.TrimSpace(description), cmd.UseLine())
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(out, "  %s [command]\n\nAvailable Commands:\n", cmd.CommandPath())
		for _, child := range cmd.Commands() {
			if child.IsAvailableCommand() {
				fmt.Fprintf(out, "  %-10s %s\n", child.Name(), child.Short)
			}
		}
	}
	if cmd.Example != "" {
		fmt.Fprintf(out, "\nExamples:\n%s\n", cmd.Example)
	}
	// These are display-only sets. Do not hide/move persistent flags or change
	// their parsing/completion; file mode still rejects operational selectors.
	local := cmd.LocalFlags()
	if cmd.Name() == "run" {
		fileFlags := pflag.NewFlagSet("file", pflag.ContinueOnError)
		sharedFlags := pflag.NewFlagSet("input", pflag.ContinueOnError)
		liveFlags := pflag.NewFlagSet("live", pflag.ContinueOnError)
		local.VisitAll(func(flag *pflag.Flag) {
			switch flag.Name {
			case "prompt", "prompt-file":
				sharedFlags.AddFlag(flag)
			case "agent", "namespace", "wait", "result-port":
				liveFlags.AddFlag(flag)
			default:
				fileFlags.AddFlag(flag)
			}
		})
		cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
			display := *flag
			if display.Name == "context" {
				display.Usage = "Kubernetes context (live only)"
			}
			liveFlags.AddFlag(&display)
		})
		writeHelpFlags(out, "Flags (file preview)", fileFlags)
		// Adjacent headings retain the short-help line budget as file flags grow.
		fmt.Fprintf(out, "Flags (shared input):\n%s", sharedFlags.FlagUsagesWrapped(80))
		fmt.Fprintf(out, "Flags (live Orka):\n%s", liveFlags.FlagUsagesWrapped(80))
		return
	}
	flags := pflag.NewFlagSet("help", pflag.ContinueOnError)
	flags.AddFlagSet(local)
	if cmd.Name() == "chat" {
		flags.AddFlagSet(cmd.InheritedFlags())
	}
	writeHelpFlags(out, "Flags", flags)
}

func writeHelpFlags(out io.Writer, title string, flags *pflag.FlagSet) {
	fmt.Fprintf(out, "\n%s:\n%s", title, flags.FlagUsagesWrapped(80))
}
