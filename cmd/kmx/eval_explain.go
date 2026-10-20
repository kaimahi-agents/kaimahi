package main

import (
	"errors"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newEvalExplainCommand(state *commandState) *cobra.Command {
	var opt app.SuiteEvaluationExplainOptions
	cmd := &cobra.Command{
		Use:               "explain [failure-id]",
		Short:             "Inspect or clear private local evaluation failures offline",
		Long:              "List retained non-pass runs for the current or explicit workspace, or inspect one failure ID. Content is withheld by default; --show-content prints sanitized bounded excerpts, expected values and rationale. Local diagnostics are not evidence and cannot authorize push or Deploy. Retention is limited to 10 runs across suite revisions, 64 KiB per answer and 128 KiB per record. --clear removes one ID, or all records only with explicit --workspace DIR; it does not clear runtime/model history or passing proof.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
				return errors.New("eval explain is offline and refuses operational selectors")
			}
			if cmd.Flags().Changed("workspace") && strings.TrimSpace(opt.Workspace) == "" {
				return errors.New("--workspace requires a directory")
			}
			if opt.Clear && opt.ShowContent {
				return errors.New("--clear and --show-content are mutually exclusive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opt.ID = args[0]
			}
			if opt.Clear && opt.ID == "" && !cmd.Flags().Changed("workspace") {
				return errors.New("clearing all failure diagnostics requires explicit --workspace DIR")
			}
			a := &app.App{Out: state.deps.stdout, Err: state.deps.stderr}
			return a.ExplainSuiteEvaluation(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Workspace, "workspace", "", "exact workspace root; otherwise discover nearest workspace")
	cmd.Flags().BoolVar(&opt.ShowContent, "show-content", false, "print sanitized bounded excerpts, expected values and judge rationale")
	cmd.Flags().BoolVar(&opt.Clear, "clear", false, "clear one failure ID, or this explicitly selected workspace's retained failures")
	return cmd
}
