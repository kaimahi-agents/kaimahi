package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// newAXCommand exposes AX as an evaluation target, not as a second platform
// Kaimahi claims to install or support. AX's published release has no binary or
// deployment assets: its documented path builds images from source with ko,
// pushes them to an operator-owned registry, and assumes Agent Substrate is
// already installed. `status` can inspect that result honestly; an `install`
// verb would have to invent upstream artifacts and ownership that do not exist.
func newAXCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "ax",
		Short: "Inspect an externally installed Google AX evaluation",
		Long: `Inspect an externally installed Google AX evaluation.

kmx does not install AX. AX currently requires a source build, ko, a writable
image registry, and an existing Agent Substrate deployment; its GitHub release
contains no deployable assets for kmx to pin and apply.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	group.AddCommand(newAXStatusCommand(state))
	return group
}

func newAXStatusCommand(state *commandState) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect AX components and the Substrate services they name",
		Args:  cobra.NoArgs,
		RunE:  appRun(state, func(a *app.App) error { return a.AXStatus(namespace) }),
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", app.AXNamespace,
		"namespace where AX is installed")
	return cmd
}
