package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func newStatusCommand(state *commandState) *cobra.Command {
	var destination, workspace, directory string
	var opt app.DestinationStatusOptions
	var all bool
	cmd := &cobra.Command{
		Use: "status [--to <destination>]", Short: "Alias of local list, or inspect a deployment destination",
		Long: "Without --to, status is a compatibility alias of kmx list: running local instances, with --all for stopped ones. Use inspect ID for detail and info for inference setup.\nWith --to, inspect deployed agents at a saved destination. Use kmx orka status for the Orka runtime report.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("to") && strings.TrimSpace(destination) == "" {
				return fmt.Errorf("--to requires a destination name; omit --to for local status")
			}
			if opt.Output != "table" && opt.Output != "json" {
				return fmt.Errorf("status output must be table or json")
			}
			if cmd.Flags().Changed("workspace") && strings.TrimSpace(workspace) == "" {
				return fmt.Errorf("status requires non-empty --workspace")
			}
			if cmd.Flags().Changed("directory") && strings.TrimSpace(directory) == "" {
				return fmt.Errorf("--directory requires a directory")
			}
			if destination == "" {
				if cmd.Flags().Changed("workspace") || state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
					return fmt.Errorf("local status uses --directory; use --to NAME for destination selectors")
				}
			} else if cmd.Flags().Changed("directory") {
				return fmt.Errorf("--directory is for local status; use --workspace with --to")
			}
			if destination != "" && cmd.Flags().Changed("all") {
				return fmt.Errorf("--all applies to local inventory, not --to")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if destination == "" {
				return app.ListAgentInstances(cmd.OutOrStdout(), directory, opt.Output, all)
			}
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
			if target.Runtime == "agentsessions" {
				return fmt.Errorf("status does not support runtime agentsessions")
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
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include stopped local instances (without --to)")
	cmd.Flags().StringVar(&directory, "directory", "", "filter instances by original source directory (default: all local instances)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "select an exact workspace root instead of nearest-ancestor discovery")
	_ = cmd.RegisterFlagCompletionFunc("workspace", staticCompletion(nil))
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	return cmd
}
