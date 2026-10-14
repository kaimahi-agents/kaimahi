package main

import (
	"errors"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
	"os"
)

func newConsoleCommand(state *commandState) *cobra.Command {
	var opt app.AgentTUIOptions
	var suite app.SuiteConsoleOptions
	var attestations bool
	var deployment string
	cmd := &cobra.Command{
		Use: "console", Short: "Open the interactive workspace for agents and environments", Args: cobra.NoArgs,
		Long: "Browse one local kind and one remote Kubernetes environment side by side.\nThe console lists and chats with native Orka Agents.\nCreation reviews authored source/all cases and a named Ready openai Provider.\nAfter execution consent, full eval and exact publication lead to a reviewed Lift Apply/Verify.\nReady placement is not an answer; evaluation/partial failures may leave resources.\nIn-place editing is retired; edit authored source, then eval, push and lift.\nUse h/j/k/l or arrows to navigate; / opens commands and argument completion.\nR opens recent read-only Orka runs for the selected Agent (caller must list/get Tasks and get the kube-system Namespace).\nLift moved: kmx lift <ref>@<digest> --to-context … --evidence …\nUse --demo to explore with sample data and no cluster access.",
	}
	cmd.Long += "\n\n--workspace (or the nearest workspace.yaml ancestor) preselects the authored source in the shared lifecycle review pane. Image execution uses the same review/eval/lift flow when its profile is supported."
	cmd.Flags().StringVar(&opt.LocalContext, "local-context", "", "local kind context (default: last TUI selection, configured context, or first local)")
	cmd.Flags().StringVar(&opt.RemoteContext, "remote-context", "", "remote context (default: last TUI selection, last lift target, configured context, or first remote)")
	cmd.Flags().StringVar(&opt.Namespace, "namespace", app.OrkaNamespace, "Orka namespace to display")
	cmd.Flags().BoolVar(&opt.Demo, "demo", false, "use sample agents; no cluster or cloud operations")
	cmd.Flags().StringVar(&deployment, "deployment", "", "open shared chat for a retained image-suite placement")
	cmd.Flags().StringVar(&suite.Workspace, "workspace", "", "experimental image-suite source workspace")
	cmd.Flags().StringVar(&suite.Registry, "registry", "", "container registry/repository prefix")
	cmd.Flags().BoolVar(&suite.PlainHTTP, "plain-http", false, "development registry HTTP")
	cmd.Flags().StringVar(&suite.Build.Builder, "builder", "", "Docker buildx builder for image suites")
	cmd.Flags().BoolVar(&attestations, "attestations", true, "request image SBOM and provenance")
	cmd.Flags().BoolVar(&suite.Build.RequireAttestations, "require-attestations", false, "require image attestations")
	_ = cmd.RegisterFlagCompletionFunc("local-context", completeContexts)
	_ = cmd.RegisterFlagCompletionFunc("remote-context", completeContexts)
	cmd.RunE = appRun(state, func(a *app.App) error {
		if deployment != "" {
			if opt.Demo || suite.Workspace != "" {
				return fmt.Errorf("--deployment cannot be combined with --demo or --workspace")
			}
			return a.ChatImagePlacement(deployment)
		}
		if suite.Workspace == "" && !opt.Demo {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := workspace.Discover(cwd, "")
			if err == nil {
				suite.Workspace = root
			} else if !errors.Is(err, workspace.ErrNotFound) {
				return err
			}
		}
		if suite.Workspace != "" {
			if opt.Demo {
				return fmt.Errorf("--workspace performs real operations; omit --demo")
			}
			suite.Build.DisableAttestations = !attestations
			opt.Workspace = suite.Workspace
			opt.Suite = suite
		}
		return a.AgentTUI(opt)
	})
	return cmd
}
