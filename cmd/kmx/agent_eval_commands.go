package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func newAgentEvalCommand() *cobra.Command {
	group := &cobra.Command{Use: "eval", Short: "Prepare reference cases and compare saved evaluation evidence", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
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
	var opt app.SessionReferenceExtractionOptions
	from := &cobra.Command{
		Use:   "from-sessions <session-uid>...",
		Short: "Extract private single-turn session references without copying recorded content",
		Long: "Read one or more completed, fresh text-chat sessions and bind their verified journal prefixes\n" +
			"to authored assertion-only YAML (id, expectContains and/or assertions; no input).\n" +
			"Write private UID/digest references to --out; an existing manifest is atomically appended.\n" +
			"Duplicates refuse the whole operation. No inference, recorded inputs or answers are exported.\n" +
			"Case IDs combine the authored id with a stable source UID digest. References inherit source TTL.\n" +
			"Keep manifests private; endpoint selection is not data-owner authorization.",
		Args: usageArgs(1, 1000, "kmx agent eval from-sessions <session-uid>... --sessions <source> --assertions <case.yaml> --out <manifest.json>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.SessionUIDs = args
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Run: &run.Runner{Context: cmd.Context()}}
			return a.ExtractAgentSessionReferences(opt)
		},
	}
	from.Flags().StringVar(&opt.Sessions, "sessions", "", "explicit source agentsessions host:port")
	from.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified source TLS")
	from.Flags().StringVar(&opt.Assertions, "assertions", "", "authored assertion-only YAML applied to each selected UID")
	from.Flags().StringVar(&opt.Out, "out", "", "private reference manifest path (existing file means append)")
	for _, name := range []string{"sessions", "assertions", "out"} {
		_ = from.MarkFlagRequired(name)
	}
	_ = from.RegisterFlagCompletionFunc("sessions", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
	group.AddCommand(diff, from)
	return group
}
