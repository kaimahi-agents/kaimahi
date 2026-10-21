package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newSuiteEvalExportCommand(state *commandState) *cobra.Command {
	var opt app.SuiteEvaluationExportOptions
	cmd := &cobra.Command{
		Use:   "export --member <id> --out <file>",
		Short: "Export portable passing evidence for one exact suite member",
		Long: "Export the latest completed passing evaluation for the exact prepared suite and explicitly selected member, without loading cluster configuration or calling a runtime, model or registry.\n" +
			"The new detached JSON document contains public identity and result metadata, not prompts, answers, reasoning, endpoints or credentials. It is unsigned consistency evidence, not authentication or deployment authority.\n" +
			"Fresh managed evaluation must capture Go build provenance: a tagged module version with its module checksum (including go install module@version), or a full VCS revision with an explicit unmodified state. Modified builds, unstamped development builds and pseudo-versions without a clean revision refuse. Use an identified release/module build or clean checkout, then evaluate again. A Linux binary digest is optional, never required.\n" +
			"Legacy records without captured metadata refuse rather than invent timestamps. Private --evaluation push and Deploy gates are unchanged; this public document does not replace private receipts.\n" +
			"--out must name a new file outside the workspace, suite cache and managed passing state. Existing files are never replaced. Select --workspace exactly or discover the nearest workspace.yaml.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("eval export accepts no positional references")
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateContainerEngineFlag(cmd, args); err != nil {
				return err
			}
			if state.contextFlag != "" {
				return errors.New("--context cannot be used with eval export")
			}
			if flag := cmd.Flag("container-engine"); flag != nil && flag.Changed {
				return errors.New("eval export does not use --container-engine")
			}
			for _, name := range []string{"workspace", "member", "out"} {
				if flag := cmd.Flag(name); flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("eval export requires non-empty --%s", name)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			return a.ExportSuiteEvaluation(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Workspace, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	cmd.Flags().StringVar(&opt.Member, "member", "", "exact evaluated member identifier")
	cmd.Flags().StringVar(&opt.Out, "out", "", "new detached public evidence file outside workspace/cache/managed state")
	_ = cmd.MarkFlagRequired("member")
	_ = cmd.MarkFlagRequired("out")
	_ = cmd.MarkFlagDirname("workspace")
	_ = cmd.MarkFlagFilename("out", "json")
	_ = cmd.RegisterFlagCompletionFunc("member", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}
