package main

import (
	"fmt"
	"strings"

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
// It is a separate command from `kmx cluster up` rather than the only way to get
// Orka. `up` installs the pinned release as its own runtime step, on the
// cluster it just created; this group is how the same install reaches a
// cluster kmx did not bring up, and how that install is inspected. What it
// does NOT carry is the runtime identity: `kmx cluster up --step orka` provisions the
// Task result account, and a standalone install deliberately does not, so
// installing somebody else's platform — 17 CRDs, four
// ValidatingAdmissionPolicies and two Deployments — never mints a grant as a
// side effect.
func newOrkaCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use:   "orka",
		Short: "Install and inspect Orka, the agent platform",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	group.AddCommand(newOrkaModelCommand(state), newOrkaInstallCommand(state), newOrkaStatusCommand(state), newOrkaToolsCommand(state), newAgentShowCommand(state), newAgentListCommand(state))
	return group
}

func newOrkaInstallCommand(state *commandState) *cobra.Command {
	var opt app.OrkaOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install pinned Orka and an optional keyless Provider",
		Long:  "Install the pinned online release and an optional keyless Provider.\nUse --provider - --offline-bundle DIR for the fixed local native profile.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("offline-bundle") {
				if strings.TrimSpace(opt.OfflineBundle) == "" {
					return fmt.Errorf("--offline-bundle requires a non-empty directory")
				}
				if opt.Provider != "-" {
					return fmt.Errorf("--offline-bundle requires --provider -")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opt.OfflineBundle, "offline-bundle", "", "verified local native Orka bundle (requires --provider -)")
	_ = cmd.MarkFlagDirname("offline-bundle")
	// No --version. The digest below the flag is of ONE tag's bytes, so a
	// version flag would either carry no verification or need a digest per
	// version; the pin is the point, and moving it is an edit to this
	// repository that someone reviews.
	cmd.Flags().StringVar(&opt.Provider, "provider", "",
		"Orka Provider to create for the in-cluster model server ('-' for none; default: local)")
	cmd.Flags().StringVar(&opt.Model, "model", "", "default model that Provider resolves (default: qwen2.5:3b)")
	cmd.Flags().StringVar(&opt.ModelURL, "model-url", "",
		"OpenAI-compatible endpoint the Provider points at (default: the in-cluster Ollama)")
	cmd.Flags().BoolVar(&opt.NoApply, "no-apply", false, "verify the installer (fetch online), write nothing")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "server-side validation only")
	cmd.MarkFlagsMutuallyExclusive("no-apply", "dry-run")
	bindProvisioningOutput(cmd, state, "cluster.orka.install", func() provisioningSelection { return installSelection(opt) }, func(a *app.App) error { return a.OrkaInstall(opt) })
	return cmd
}

// The runtime report remains table-only, separate from destination inventory.
func newOrkaStatusCommand(state *commandState) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "status", Short: "Show the runtime Orka has installed, and what it can resolve", Args: cobra.NoArgs}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table"}))
	cmd.RunE = appRun(state, func(a *app.App) error { return a.StatusWithOptions(app.StatusOptions{Output: output}) })
	return cmd
}
