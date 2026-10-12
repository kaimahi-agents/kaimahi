package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func validateWorkspaceCreateFlags(cmd *cobra.Command, state *commandState) error {
	if state.contextFlag != "" {
		return fmt.Errorf("--context cannot be used with --workspace")
	}
	var invalid string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "workspace", "model", "instructions", "description":
		default:
			if invalid == "" {
				invalid = flag.Name
			}
		}
	})
	if invalid != "" {
		return fmt.Errorf("--%s cannot be used with --workspace", invalid)
	}
	return nil
}
