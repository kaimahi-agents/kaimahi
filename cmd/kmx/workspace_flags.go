package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func validateWorkspaceCreateFlags(cmd *cobra.Command, state *commandState) error {
	if state.contextFlag != "" {
		return fmt.Errorf("--context requires --live; it cannot be used with offline workspace creation")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "workspace", "model", "instructions", "description", "live":
		default:
			if invalid == "" {
				invalid = flag.Name
			}
		}
	})
	if invalid != "" {
		if invalid == "runtime" && cmd.Flag("runtime").Value.String() == "kagent" {
			return fmt.Errorf("--runtime kagent requires --live --runtime kagent; workspace creation is native-Orka only")
		}
		return fmt.Errorf("--%s requires --live; it cannot be used with offline workspace creation", invalid)
	}
	return nil
}
