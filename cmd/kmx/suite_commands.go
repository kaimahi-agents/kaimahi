package main

import (
	"fmt"

	"github.com/spf13/cobra"

	agentsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
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
	var pushPlainHTTP bool
	push := &cobra.Command{
		Use:   "push <directory> <reference>",
		Short: "Push an extracted AgentSuite to an OCI layout or registry",
		Long: "Push an extracted AgentSuite to an OCI registry, or use --to-layout for a local OCI image layout.\n\n" +
			"Registry authentication is read from the standard Docker credential store.",
		Example: "  kmx suite push ./suite registry.example.com/team:v1\n" +
			"  kmx suite push ./suite --to-layout ./layout agentsuites/team:v1",
		Args: usageArgs(2, 2, "kmx suite push <directory> [--to-layout <layout>] [--plain-http] <reference>"),
	}
	push.Flags().StringVar(&target, "to-layout", "", "local OCI image-layout target directory")
	push.Flags().BoolVar(&pushPlainHTTP, "plain-http", false, "use HTTP instead of HTTPS for a registry target")
	_ = push.MarkFlagDirname("to-layout")
	push.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		var result agentsuite.PushResult
		var err error
		if cmd.Flags().Changed("to-layout") {
			if target == "" {
				return fmt.Errorf("--to-layout cannot be empty")
			}
			if pushPlainHTTP {
				return fmt.Errorf("--plain-http cannot be used with --to-layout")
			}
			result, err = a.PushSuite(cmd.Context(), args[0], target, args[1])
		} else {
			result, err = a.PushSuiteRegistry(cmd.Context(), args[0], args[1], pushPlainHTTP)
		}
		if err != nil {
			return err
		}
		if target == "" {
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"Pushed AgentSuite %s to %s (%s)\n",
				result.Report.Name,
				result.Reference,
				result.Descriptor.Digest,
			)
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
	var pullPlainHTTP bool
	pull := &cobra.Command{
		Use:   "pull <reference>",
		Short: "Pull and extract an AgentSuite from an OCI layout or registry",
		Long: "Pull and extract an AgentSuite from an OCI registry, or use --from-layout for a local OCI image layout.\n\n" +
			"Registry authentication is read from the standard Docker credential store.",
		Example: "  kmx suite pull registry.example.com/team:v1 --output ./suite\n" +
			"  kmx suite pull agentsuites/team:v1 --from-layout ./layout --output ./suite",
		Args: usageArgs(1, 1, "kmx suite pull <reference> [--from-layout <layout>] [--plain-http] --output <directory>"),
	}
	pull.Flags().StringVar(&pullSource, "from-layout", "", "local OCI image-layout source directory")
	pull.Flags().StringVar(&pullOutput, "output", "", "extracted AgentSuite output directory")
	pull.Flags().BoolVar(&pullPlainHTTP, "plain-http", false, "use HTTP instead of HTTPS for a registry source")
	_ = pull.MarkFlagRequired("output")
	_ = pull.MarkFlagDirname("from-layout")
	_ = pull.MarkFlagDirname("output")
	pull.RunE = func(cmd *cobra.Command, args []string) error {
		a := &app.App{Out: cmd.OutOrStdout()}
		var result agentsuite.PullResult
		var err error
		source := args[0]
		if cmd.Flags().Changed("from-layout") {
			if pullSource == "" {
				return fmt.Errorf("--from-layout cannot be empty")
			}
			if pullPlainHTTP {
				return fmt.Errorf("--plain-http cannot be used with --from-layout")
			}
			result, err = a.PullSuite(cmd.Context(), pullSource, args[0], pullOutput)
			source = pullSource
		} else {
			result, err = a.PullSuiteRegistry(cmd.Context(), args[0], pullOutput, pullPlainHTTP)
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(
			cmd.OutOrStdout(),
			"Pulled and extracted AgentSuite %s from %s as %s to %s (%s)\n",
			result.Report.Name,
			source,
			result.Reference,
			result.Path,
			result.Descriptor.Digest,
		)
		return err
	}
	group.AddCommand(pull, push, validate)
	return group
}
