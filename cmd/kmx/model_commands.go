package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/spf13/cobra"
)

func newModelCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "model", Short: "Inspect local model access without starting a runtime", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	format := "table"
	list := &cobra.Command{Use: "list", Short: "List installed local models and explicit install guidance", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if format != "table" && format != "json" {
			return fmt.Errorf("unsupported output format; use table or json")
		}
		discover := state.deps.discoverModels
		if discover == nil {
			discover = modelaccess.Discover
		}
		reports := discover(cmd.Context())
		var discoveryErr error
		for _, r := range reports {
			if r.State == modelaccess.Failure {
				discoveryErr = errors.New("local model discovery incomplete; inspect provider failure reports")
				break
			}
		}
		if format == "json" {
			return errors.Join(discoveryErr, json.NewEncoder(cmd.OutOrStdout()).Encode(reports))
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "PROVIDER\tSTATE\tMODEL\tENDPOINT"); err != nil {
			return err
		}
		for _, r := range reports {
			if len(r.Models) == 0 {
				fmt.Fprintf(w, "%s\t%s\t—\t—\n", r.Provider, r.State)
			}
			for _, m := range r.Models {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Provider, r.State, m.Route.Model, m.Route.Endpoint)
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, r := range reports {
			if r.Reason != "" {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", r.Provider, r.Reason); err != nil {
					return err
				}
			}
			if r.State != modelaccess.Installed {
				note := ""
				if r.Install.Recommended {
					note = " (suggested on supported Windows)"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s install%s: %s\n  %s\n  Runtime download: %s; model download: %s. Install/download requires your action.\n", r.Provider, note, r.Install.URL, r.Install.Requirements, r.Install.RuntimeSize, r.Install.ModelSize); err != nil {
					return err
				}
			}
		}
		return discoveryErr
	}}
	list.Flags().StringVarP(&format, "output", "o", "table", "report format: table or json")
	_ = list.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	group.AddCommand(list)
	return group
}
