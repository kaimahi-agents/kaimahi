package main

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// newModelsCommand is the model seam's onboarding family, beside `tools`
// for the tool seam. One verb today; a group rather than a top-level
// `kmx model-add` because the seam is where the family boundary is, and
// the tool seam's group grew from one verb to six.
func newModelsCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "models",
		Short: "Manage governed model upstreams",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	group.AddCommand(newModelsAddCommand(state))
	return group
}

func newModelsAddCommand(state *commandState) *cobra.Command {
	var opt app.AddModelOptions
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Scaffold and add a governed model upstream",
		Args: usageArgs(1, 1,
			"kmx models add <name> --url <url including its path> --classification free|metered"),
	}
	cmd.Flags().StringVar(&opt.URL, "url", "", "endpoint's in-cluster URL, including the path clients post to")
	cmd.Flags().StringVar(&opt.Protocol, "protocol", "",
		strings.Join(scaffold.Protocols, "|")+" (needed only when the path names neither)")
	cmd.Flags().StringVar(&opt.Classification, "classification", "",
		strings.Join(scaffold.Classifications, "|")+" — explicit, never inferred")
	cmd.Flags().StringVar(&opt.ServerEgress, "server-egress", "", "none|dns|keep")
	cmd.Flags().IntVar(&opt.PodPort, "pod-port", 0, "container port for named targetPort")
	cmd.Flags().StringVar(&opt.Out, "out", "", "manifest output path ('-' for stdout)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "write and stop")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation only")
	cmd.RunE = appRun(state, func(a *app.App) error {
		opt.Name = cmd.Flags().Arg(0)
		if strings.TrimSpace(opt.URL) == "" {
			return errors.New("kmx models add: --url is required — the endpoint's own in-cluster URL, " +
				"including the path its clients post to (e.g. http://vllm.demo:8000/v1/responses)")
		}
		return a.AddModel(opt)
	})
	return cmd
}
