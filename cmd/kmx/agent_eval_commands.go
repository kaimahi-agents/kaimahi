package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentEvalCommand() *cobra.Command {
	group := &cobra.Command{Use: "eval", Short: "Compare saved evaluation evidence offline", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	diff := &cobra.Command{
		Use:   "diff <before.json> <after.json>",
		Short: "Compare assertion outcomes in two saved evaluation receipts",
		Long: "Compare saved v2 evaluation receipts without cluster configuration, network access, or model calls.\n" +
			"Reports revision identities and every case/assertion change; never prints answers or assertion operands.\n" +
			"Exits nonzero for regressions, lost or missing evidence, changed sets, or incompatible runtime identities.\n" +
			"A complete stable comparison or fixes can exit zero even when both evaluations failed.\n" +
			"Legacy receipts are recognized but cannot establish assertion-level comparisons.",
		Args: usageArgs(2, 2, "kmx agent eval diff <before.json> <after.json>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			return a.DiffAgentEvaluations(app.EvaluationDiffOptions{Before: args[0], After: args[1]})
		},
	}
	group.AddCommand(diff)
	return group
}
