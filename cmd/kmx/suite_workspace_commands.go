package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

// Image invocation remains a distinct member operation; release lift is shared
// with the existing agent command and never invokes a builder.
func addSuiteWorkspaceCommands(group *cobra.Command, state *commandState) {
	var deploymentID string
	status := &cobra.Command{Use: "status", Short: "Inspect a retained image-suite placement", Args: cobra.NoArgs}
	status.Flags().StringVar(&deploymentID, "deployment", "", "placement ID printed by lift")
	_ = status.MarkFlagRequired("deployment")
	status.RunE = appRun(state, func(a *app.App) error {
		rows, err := a.ImagePlacementStatus(status.Context(), deploymentID)
		if err != nil {
			return err
		}
		return json.NewEncoder(status.OutOrStdout()).Encode(rows)
	})
	group.AddCommand(status)
	var root, placement, member, prompt, target string
	run := &cobra.Command{Use: "run", Short: "Invoke a recorded image workspace deployment", Args: cobra.NoArgs}
	run.Flags().StringVar(&root, "workspace", "", "workspace root (default: nearest workspace.yaml ancestor)")
	run.Flags().StringVar(&placement, "deployment", "", "recorded placement name")
	run.Flags().StringVar(&member, "member", "", "agent member id")
	run.Flags().StringVar(&prompt, "prompt", "", "input for deployed agent")
	run.Flags().StringVar(&target, "to-context", "", "disambiguate recorded context")
	_ = run.MarkFlagRequired("deployment")
	_ = run.MarkFlagRequired("member")
	_ = run.MarkFlagRequired("prompt")
	run.RunE = appRun(state, func(a *app.App) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		resolved, err := workspace.Discover(cwd, root)
		if err != nil {
			return err
		}
		summary, err := workspace.Describe(resolved)
		if err != nil {
			return err
		}
		records, err := app.ReadWorkspaceDeployments(resolved, summary.Name)
		if err != nil {
			return err
		}
		var matches []app.WorkspaceDeployment
		for _, record := range records {
			if record.Environment.Name == placement && (target == "" || record.Environment.Context == target) {
				matches = append(matches, record)
			}
		}
		if len(matches) != 1 {
			return errors.New("placement absent or ambiguous; specify --to-context")
		}
		answer, err := a.RunSuiteDeployment(run.Context(), matches[0], member, prompt)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.Out, answer)
		return err
	})
	group.AddCommand(run)
}
