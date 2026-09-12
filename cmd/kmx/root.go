package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/planebuild"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

type dependencies struct {
	stdin      *os.File
	stdout     io.Writer
	stderr     io.Writer
	loadConfig func(string) (*config.Config, error)
	newApp     func(*config.Config) *app.App
	buildInfo  func() (*debug.BuildInfo, bool)
}

func productionDependencies() dependencies {
	return dependencies{os.Stdin, os.Stdout, os.Stderr, config.Load, app.New, debug.ReadBuildInfo}
}

type commandState struct {
	deps        dependencies
	contextFlag string
	app         *app.App
	argv        []string
}

func (s *commandState) application() (*app.App, error) {
	if s.app != nil {
		return s.app, nil
	}
	cfg, err := s.deps.loadConfig(s.contextFlag)
	if err != nil {
		return nil, err
	}
	a := s.deps.newApp(cfg)
	a.Out, a.Err, a.Stdin = s.deps.stdout, s.deps.stderr, s.deps.stdin
	a.Run.Stdout, a.Run.Stderr = s.deps.stdout, s.deps.stderr
	s.app = a
	return a, nil
}

func execute(argv []string, deps dependencies) error {
	contextFlag := ""
	var err error
	if len(argv) == 0 || argv[0] != "__complete" && argv[0] != "__completeNoDesc" {
		argv, contextFlag, err = extractContext(argv)
		if err != nil {
			return err
		}
	}
	state := &commandState{deps: deps, contextFlag: contextFlag, argv: append([]string(nil), argv...)}
	root := newRootCommand(state)
	root.SetArgs(argv)
	return root.Execute()
}

func newRootCommand(state *commandState) *cobra.Command {
	root := &cobra.Command{
		Use:           "kmx",
		Short:         "Create and run governed agents on Kubernetes",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(state.deps.stdout)
	root.SetErr(state.deps.stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().String("context", "", "act on this kube context for one command (may appear anywhere)")
	_ = root.RegisterFlagCompletionFunc("context", completeContexts)
	root.AddCommand(
		newVersionCommand(state), newCompletionCommand(root), newCtxCommand(state),
		newQuickstartCommand(state), newUpCommand(state), newLiftCommand(state),
		newPlaneCommand(state), newGovernCommand(state), newCredentialsCommand(state), newCredentialCommand(state),
		newLedgerCommand(state), newGrantsCommand(state), newAuditCommand(state), newFlowCommand(state),
		newWatchCommand(state),
		newUseCommand(state),
		newBudgetCommand(state), newApprovalsCommand(state), newApproveCommand(state), newDenyCommand(state),
		newRequestCommand(state), newModelsCommand(state), newMigrateCommand(state),
		newOrkaCommand(state),
		newBackupCommand(state), newRestoreCommand(state),
		newMetricsCommand(state), newStatusCommand(state), newDownCommand(state), newAgentCommand(state),
	)
	return root
}

func appRun(state *commandState, fn func(*app.App) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a, err := state.application()
		if err != nil {
			return err
		}
		// Chat slash commands are separate operations; replaying the outer chat
		// command would not repeat the mutation they are asking to confirm.
		if cmd.Name() != "chat" && len(state.argv) > 0 {
			parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine),
				"CRED=" + quoteShell(a.Cfg.Credential),
				"kmx", "--context", quoteShell(a.Cfg.KubeContext)}
			for _, arg := range state.argv {
				parts = append(parts, quoteShell(arg))
			}
			a.InvocationCommand = strings.Join(parts, " ")
		}
		return fn(a)
	}
}

func quoteShell(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func usageArgs(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || max >= 0 && len(args) > max {
			return errors.New("usage: " + usage)
		}
		return nil
	}
}

func newVersionCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Show this build and the component versions it targets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		info, ok := state.deps.buildInfo()
		revision := "unknown (kmx plane needs --source <checkout>)"
		if rev, err := planebuild.Revision(info, ok); err == nil {
			revision = rev
		}
		fmt.Fprintf(cmd.OutOrStdout(), "kmx %s\n  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md\n  kagent   %s\n  model    %s\n  plane    %s, built from %s\n",
			version.Resolve(info, ok), config.DefaultKagentVersion, config.DefaultModel, app.PlaneImage, revision)
		return nil
	}}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{Use: "completion bash|zsh|fish", Short: "Generate shell completion", Args: cobra.ExactArgs(1)}
	cmd.ValidArgs = []string{"bash", "zsh", "fish"}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
		case "zsh":
			return root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return root.GenFishCompletion(cmd.OutOrStdout(), true)
		default:
			return fmt.Errorf("unsupported shell %q — use bash, zsh, or fish", args[0])
		}
	}
	return cmd
}

func parseOptionalCredential(args []string, fallback string) string {
	if len(args) == 1 {
		return args[0]
	}
	return fallback
}
