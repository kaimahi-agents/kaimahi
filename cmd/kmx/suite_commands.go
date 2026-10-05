package main

import (
	"github.com/spf13/cobra"
)

func newSuiteCommand(state *commandState) *cobra.Command {
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
	validate.RunE = func(*cobra.Command, []string) error {
		return state.offlineApplication().ValidateSuite(validate.Flags().Arg(0), output)
	}
	group.AddCommand(validate)
	return group
}
