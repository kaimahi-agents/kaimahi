package main

import (
	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

// newOrkaCommand is the front door for a platform this project did not
// write.
//
// A group rather than a top-level verb, and named for the thing rather than
// abstracted into `kmx platform install <name>`: there is one platform here,
// and an interface over one implementation is the most expensive kind. When
// a second one earns the abstraction it can have it.
//
// It is deliberately NOT part of `kmx up`. `up` brings up this project's own
// runtime; installing somebody else's platform is a separate decision with a
// separate blast radius — 17 CRDs, four ValidatingAdmissionPolicies and two
// Deployments — and folding it in would make one command mean two things.
func newOrkaCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "orka",
		Short: "Install and inspect Orka, the agent platform kmx can govern",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	group.AddCommand(newOrkaInstallCommand(state), newOrkaStatusCommand(state))
	return group
}

func newOrkaInstallCommand(state *commandState) *cobra.Command {
	var opt app.OrkaOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Orka " + app.OrkaVersion + " and a keyless Provider",
		Args:  cobra.NoArgs,
	}
	// No --version. The digest below the flag is of ONE tag's bytes, so a
	// version flag would either carry no verification or need a digest per
	// version; the pin is the point, and moving it is an edit to this
	// repository that someone reviews.
	cmd.Flags().StringVar(&opt.Provider, "provider", "",
		"Orka Provider to create for the in-cluster model server ('-' for none; default: local)")
	cmd.Flags().StringVar(&opt.Model, "model", "", "default model that Provider resolves (default: qwen2.5:3b)")
	cmd.Flags().StringVar(&opt.ModelURL, "model-url", "",
		"OpenAI-compatible endpoint the Provider points at (default: the in-cluster Ollama)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "fetch and verify the installer, write nothing")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation only")
	cmd.RunE = appRun(state, func(a *app.App) error { return a.OrkaInstall(opt) })
	return cmd
}

func newOrkaStatusCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report what Orka has installed, and what it can resolve",
		Args:  cobra.NoArgs,
		RunE:  appRun(state, func(a *app.App) error { return a.OrkaStatus() }),
	}
}
