package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newSuiteCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "suite",
		Short: "Work with portable AgentSuite artifacts",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var output string
	validate := &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a portable AgentSuite artifact",
		Args:  usageArgs(1, 1, "kmx suite validate <path> [-o text|json]"),
	}
	validate.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	_ = validate.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	validate.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		return a.ValidateSuite(args[0], output)
	}

	var target string
	push := &cobra.Command{
		Use:   "push <directory> <reference>",
		Short: "Push an extracted AgentSuite to a local OCI image layout",
		Long: "Push an extracted AgentSuite to a local OCI image layout.\n\n" +
			"The --to-layout value is a filesystem directory. Registry targets are not supported.",
		Example: "  kmx suite push ./suite --to-layout ./layout agentsuites/team:v1",
		Args:    usageArgs(2, 2, "kmx suite push <directory> --to-layout <layout> <reference>"),
	}
	push.Flags().StringVar(&target, "to-layout", "", "local OCI image-layout target directory")
	_ = push.MarkFlagRequired("to-layout")
	_ = push.MarkFlagDirname("to-layout")
	push.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		result, err := a.PushSuite(cmd.Context(), args[0], target, args[1])
		if err != nil {
			return err
		}
		if result.Updated {
			if _, err := fmt.Fprintf(
				cmd.OutOrStdout(),
				"Updated existing local OCI layout %s; unrelated references were preserved\n",
				result.Path,
			); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Created local OCI layout %s\n", result.Path); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(
			cmd.OutOrStdout(),
			"Pushed AgentSuite %s to %s as %s (%s)\n",
			result.Report.Name,
			result.Path,
			result.Reference,
			result.Descriptor.Digest,
		)
		return err
	}

	var pullSource, pullOutput string
	pull := &cobra.Command{
		Use:   "pull <reference>",
		Short: "Pull and extract an AgentSuite from a local OCI image layout",
		Long: "Pull and extract an AgentSuite from a local OCI image layout.\n\n" +
			"The --from-layout value is a filesystem directory. Registry sources are not supported.",
		Example: "  kmx suite pull agentsuites/team:v1 --from-layout ./layout --output ./suite",
		Args:    usageArgs(1, 1, "kmx suite pull <reference> --from-layout <layout> --output <directory>"),
	}
	pull.Flags().StringVar(&pullSource, "from-layout", "", "local OCI image-layout source directory")
	pull.Flags().StringVar(&pullOutput, "output", "", "extracted AgentSuite output directory")
	_ = pull.MarkFlagRequired("from-layout")
	_ = pull.MarkFlagRequired("output")
	_ = pull.MarkFlagDirname("from-layout")
	_ = pull.MarkFlagDirname("output")
	pull.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		result, err := a.PullSuite(cmd.Context(), pullSource, args[0], pullOutput)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(
			cmd.OutOrStdout(),
			"Pulled and extracted AgentSuite %s from %s as %s to %s (%s)\n",
			result.Report.Name,
			pullSource,
			result.Reference,
			result.Path,
			result.Descriptor.Digest,
		)
		return err
	}
	group.AddCommand(pull, push, validate)
	return group
}
