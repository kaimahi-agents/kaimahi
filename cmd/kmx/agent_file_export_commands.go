package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func newAgentRunExportCommand(state *commandState) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export FILE --out FILE",
		Short: "Export a frozen canonical run profile without execution",
		Long:  "Freeze one canonical agent file's instructions, authored model and complete examples with hashes. Target examples are marked not-run. No configuration, model, judge, registry or runtime is contacted.\n\nThe content-bearing JSON is written to a new mode 0600 file in an existing private mode 0700 directory; existing files and symlink paths refuse. Native Windows private publication is not supported. Stdout contains only hash/model/status metadata. This is not a package, passing evidence or Deploy authority; actual target model selection is separate.",
		Args:  usageArgs(1, 1, "kmx run export FILE --out FILE"),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" {
				return errors.New("run export refuses --context")
			}
			if flag := cmd.Flag("container-engine"); flag != nil && flag.Changed {
				return errors.New("run export refuses --container-engine")
			}
			if strings.TrimSpace(out) == "" {
				return errors.New("run export requires a nonblank --out file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			summary, err := (&app.App{}).ExportAgentRunProfile(cmd.Context(), app.AgentRunProfileExportOptions{File: args[0], Out: out})
			if err != nil {
				return err
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(summary); err != nil {
				return errors.New("run profile summary write failed; output may exist")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "new content-bearing JSON file in an existing private directory")
	_ = cmd.MarkFlagRequired("out")
	_ = cmd.MarkFlagFilename("out", "json")
	return cmd
}
