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
		Long:              "List retained non-pass runs for the current or explicit workspace, or select one agent file with --file FILE. Content is withheld by default; --show-content prints sanitized bounded excerpts and expected values, plus retained rationale where available. File records identify the exact executed revision and report whether it is current; file judge rationale is not retained. Local diagnostics are not evidence and cannot authorize push or Deploy. Retention is limited to 10 runs per selected subject, 64 KiB per answer and 128 KiB per record. --clear removes one ID, or all records only with explicit --workspace DIR or --file FILE; it does not clear journals, source history, runtime/model history or passing proof.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
				return errors.New("eval explain is offline and refuses operational selectors")
			}
			if cmd.Flags().Changed("workspace") && strings.TrimSpace(opt.Workspace) == "" {
				return errors.New("--workspace requires a directory")
			}
			if cmd.Flags().Changed("file") && strings.TrimSpace(opt.File) == "" {
				return errors.New("--file requires an agent file")
			}
			if cmd.Flags().Changed("workspace") && cmd.Flags().Changed("file") {
				return errors.New("--workspace and --file are mutually exclusive")
			}
			if cmd.Flags().Changed("tests") && (!cmd.Flags().Changed("file") || strings.TrimSpace(opt.TestsFile) == "") {
				return errors.New("--tests requires --file FILE and a nonempty tests file")
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
			if opt.Clear && opt.ID == "" && !cmd.Flags().Changed("workspace") && !cmd.Flags().Changed("file") {
				return errors.New("clearing all failure diagnostics requires explicit --workspace DIR or --file FILE")
			}
			a := &app.App{Out: state.deps.stdout, Err: state.deps.stderr}
			return a.ExplainSuiteEvaluation(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Workspace, "workspace", "", "exact workspace root; otherwise discover nearest workspace")
	cmd.Flags().StringVar(&opt.File, "file", "", "exact agent file whose retained Test failures to inspect")
	cmd.Flags().StringVar(&opt.TestsFile, "tests", "", "Current tests file override (requires --file)")
	cmd.Flags().BoolVar(&opt.ShowContent, "show-content", false, "print sanitized bounded excerpts, expected values and retained rationale")
	cmd.Flags().BoolVar(&opt.Clear, "clear", false, "clear one failure ID, or this explicitly selected workspace/file subject's retained failures")
	cmd.MarkFlagsMutuallyExclusive("workspace", "file")
	return cmd
}
