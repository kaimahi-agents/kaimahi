package main

import (
	"encoding/json"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/spf13/cobra"
)

func newModelMatchCommand() *cobra.Command {
	var requirements modelaccess.Requirements
	cmd := &cobra.Command{Use: "match PROFILE", Short: "Match a model profile to required capabilities", Args: cobra.ExactArgs(1)}
	cmd.Flags().IntVar(&requirements.ContextTokens, "require-context", 0, "Minimum context tokens")
	cmd.Flags().IntVar(&requirements.OutputTokens, "require-output", 0, "Minimum output tokens")
	cmd.Flags().BoolVar(&requirements.Reasoning, "require-reasoning", false, "Require reasoning capability")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := requirements.Validate(); err != nil {
			return err
		}
		profile, err := modelaccess.LoadProfile(args[0])
		if err != nil {
			return err
		}
		match := requirements.Match(profile.Capabilities)
		result := struct {
			Status       string                   `json:"status"`
			Source       string                   `json:"source"`
			Provider     string                   `json:"provider"`
			Model        string                   `json:"model"`
			Requirements modelaccess.Requirements `json:"requirements"`
			Capabilities modelaccess.Capabilities `json:"capabilities"`
		}{match, "operator-declared", profile.Route.Provider, profile.Route.Model, requirements, profile.Capabilities}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
		if match != "match" {
			return fmt.Errorf("model capabilities %s", match)
		}
		return nil
	}
	return cmd
}
