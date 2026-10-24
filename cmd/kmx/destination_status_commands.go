package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func newStatusCommand(state *commandState) *cobra.Command {
	var destination, workspace string
	var opt app.DestinationStatusOptions
	cmd := &cobra.Command{
		Use: "status --to <destination>", Short: "Inspect deployed agents at a named destination",
		Long: "Inspect deployed agents across a saved destination namespace using read-only observations. Readiness is not an answer or evaluation. Use kmx orka status for the Orka runtime report.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(destination) == "" {
				return fmt.Errorf("status requires --to: use kmx status --to <destination> or kmx orka status for the runtime report")
			}
			if opt.Output != "table" && opt.Output != "json" {
				return fmt.Errorf("status output must be table or json")
			}
			if cmd.Flags().Changed("workspace") && strings.TrimSpace(workspace) == "" {
				return fmt.Errorf("status requires non-empty --workspace")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, doc, err := resolveDestinations(workspace)
			if err != nil {
				return err
			}
			target, err := doc.Target(destination)
			if err != nil {
				return err
			}
			if target.Namespace == "" {
				return fmt.Errorf("status requires an explicit saved namespace in kmx.yaml for the destination")
			}
			// Freeze only the inspection selectors before configuration can consult
			// remembered or environment contexts. No suite or binding load is needed.
			selected := *state
			if selected.contextFlag == "" {
				selected.contextFlag = target.Context
			}
			opt.Destination, opt.Namespace, opt.ExpectedRuntime = destination, target.Namespace, target.Runtime
			a, err := selected.operationApplication(cmd)
			if err != nil {
				return err
			}
			a.Cfg.ContextSource = config.SourceFlag
			if state.contextFlag == "" {
				a.Cfg.ContextSource = "kmx.yaml (--to " + destination + ")"
			}
			return a.DestinationStatus(opt)
		},
	}
	registerDestinationFlag(cmd, &destination)
	cmd.Flags().StringVar(&workspace, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	_ = cmd.RegisterFlagCompletionFunc("workspace", staticCompletion(nil))
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	return cmd
}
