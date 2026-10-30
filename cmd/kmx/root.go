package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentkitbuilder "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

type dependencies struct {
	fileRunner         agentpreview.Runner
	stdin              *os.File
	stdout             io.Writer
	stderr             io.Writer
	executable         string
	loadConfig         func(string, string) (*config.Config, error)
	newApp             func(*config.Config) *app.App
	buildInfo          func() (*debug.BuildInfo, bool)
	newAgentKitBuilder func(agentkitbuilder.Options) kmxsuite.SandboxBuilder
	discoverModels     func(context.Context) []modelaccess.Report
	newCopilot         func() copilotAccess
	openGitHubSummary  func() (io.WriteCloser, error)
	goos               string
}

func productionDependencies() dependencies {
	return dependencies{
		fileRunner: newFileMCPRunner(app.NewLocalFileRunner()),
		stdin:      os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		executable:        invocationExecutable(os.Args[0]),
		goos:              runtime.GOOS,
		openGitHubSummary: func() (io.WriteCloser, error) { return openGitHubSummary() },
		loadConfig:        config.LoadWithOverrides, newApp: app.New, buildInfo: debug.ReadBuildInfo,
		newAgentKitBuilder: func(options agentkitbuilder.Options) kmxsuite.SandboxBuilder {
			return agentkitbuilder.New(options)
		},
	}
}

// invocationExecutable keeps installed names concise, but makes a path-based
// checkout invocation independent of the directory a recovery runs from. It
// deliberately does not resolve symlinks.
func invocationExecutable(value string) string {
	if !strings.ContainsRune(value, filepath.Separator) {
		return value
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return absolute
}

type commandState struct {
	deps                dependencies
	contextFlag         string
	containerEngineFlag string
	app                 *app.App
	argv                []string
	evalNoticeShown     bool
	windowsFilePair     *agentfile.Pair
}

func (s *commandState) executable() string {
	if s.deps.executable == "" {
		return "kmx"
	}
	return s.deps.executable
}

func (s *commandState) application() (*app.App, error) {
	if s.app != nil {
		return s.app, nil
	}
	cfg, err := s.deps.loadConfig(s.contextFlag, s.containerEngineFlag)
	if err != nil {
		return nil, err
	}
	a := s.deps.newApp(cfg)
	a.Out, a.Err, a.Stdin = s.deps.stdout, s.deps.stderr, s.deps.stdin
	a.Executable = s.executable()
	a.Run.Stdout, a.Run.Stderr = s.deps.stdout, s.deps.stderr
	s.app = a
	return a, nil
}

// operationApplication binds context and identity to this invocation's App.
// Copy its Runner so shared runners retain their context.
func (s *commandState) operationApplication(cmd *cobra.Command) (*app.App, error) {
	a, err := s.application()
	if err != nil {
		return nil, err
	}
	runner := *a.Run
	runner.Context = cmd.Context()
	a.Run = &runner
	// Chat slash commands are separate operations; replaying the outer chat
	// command would not repeat the mutation they are asking to confirm.
	if cmd.Name() != "chat" && cmd.Name() != "console" && len(s.argv) > 0 {
		parts := []string{"KIND_CLUSTER=" + quoteShell(a.Cfg.KindCluster), "CONTAINER_ENGINE=" + quoteShell(a.Cfg.ContainerEngine),
			quoteShell(s.executable()), "--context", quoteShell(a.Cfg.KubeContext)}
		for _, arg := range s.argv {
			parts = append(parts, quoteShell(arg))
		}
		a.InvocationCommand = strings.Join(parts, " ")
	}
	return a, nil
}

func execute(argv []string, deps dependencies) error {
	contextFlag := ""
	var err error
	if len(argv) == 0 || argv[0] != "__complete" && argv[0] != "__completeNoDesc" {
		original := argv
		argv, contextFlag, err = extractContext(argv)
		if err != nil {
			// No Cobra hook runs after preprocessing refuses. Preserve that
			// refusal, but still disclose a recognizable legacy invocation.
			notice := ""
			if legacyCommandInvocation(original, "lift") {
				notice = "kmx lift is deprecated; use kmx package deploy (same arguments)."
			} else if legacyCommandInvocation(original, "eval") {
				notice = evalDeprecationNotice
			}
			if notice != "" {
				diagnostics := deps.stderr
				if diagnostics == nil {
					diagnostics = os.Stderr
				}
				if _, noticeErr := fmt.Fprintln(diagnostics, notice); noticeErr != nil {
					return errors.Join(err, noticeErr)
				}
			}
			return err
		}
	}
	state := &commandState{deps: deps, contextFlag: contextFlag, argv: append([]string(nil), argv...)}
	root := newRootCommand(state)
	root.InitDefaultHelpCmd()
	help, _, _ := root.Find([]string{"help"})
	help.Hidden = true
	// Cobra otherwise reports an unknown help topic on stdout but exits zero.
	help.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			topic, remaining, err := root.Find(args)
			if err != nil {
				return err
			}
			if len(remaining) != 0 {
				return fmt.Errorf("unknown help topic %q for %q", strings.Join(args, " "), topic.CommandPath())
			}
		}
		if legacyCommandInvocation(argv, "eval") {
			return state.noticeLegacyEval(cmd)
		}
		return nil
	}
	// Explicit help parses its own flags and validates global flags before
	// dispatching to a topic's HelpFunc. Cover those refusals without hooks.
	help.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if legacyCommandInvocation(argv, "eval") {
			return errors.Join(state.noticeLegacyEval(cmd), err)
		}
		if legacyCommandInvocation(argv, "lift") {
			return err
		}
		return flagErrorWithHelp(cmd, state, err)
	})
	root.SetArgs(argv)
	return root.Execute()
}

// Recognize only the root command (or explicit help topic) after known global
// flags. Unknown flags, other commands and -- stop discovery: their arguments
// may contain an opaque value equal to a legacy verb and are never searched.
func legacyCommandInvocation(argv []string, verb string) bool {
	helpTopic := false
	for i := 0; i < len(argv); i++ {
		name, _, inline := strings.Cut(argv[i], "=")
		switch name {
		case "--context", "-context", "--container-engine":
			if !inline {
				i++
			}
		case "--help", "-h":
			continue
		case "help":
			if inline || helpTopic {
				return false
			}
			helpTopic = true
		default:
			return argv[i] == verb
		}
	}
	return false
}

func newRootCommand(state *commandState) *cobra.Command {
	root := &cobra.Command{
		Use:   "kmx",
		Short: "Write, run and test agent files.",
		// Leave Args unset so Cobra rejects unknown roots before parsing their
		// flags or honoring --help. Bare root still runs the help handler.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(state.deps.stdout)
	root.SetErr(state.deps.stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().String("context", "", "Kubernetes context for live/cluster operations")
	root.PersistentFlags().StringVar(&state.containerEngineFlag, "container-engine", "", "kind engine: docker or podman")
	root.PersistentPreRunE = validateContainerEngineFlag
	_ = root.RegisterFlagCompletionFunc("context", completeContexts)
	_ = root.RegisterFlagCompletionFunc("container-engine", staticCompletion([]string{"docker", "podman"}))
	root.AddCommand(
		newVersionCommand(state), newMCPCommand(state), newCompletionCommand(root), newModelCommand(state),
		newFileCreateCommand(state), newAgentChatCommand(state), newAgentRunCommand(state),
		newAgentTestCommand(state), newAgentEvalCommand(state), newReleaseLiftCommand(state),
		newClusterCommand(state), newPackageCommand(state),
	)
	configureCommandHelp(root, state)
	installPlatformGate(root, state)
	return root
}

func validateContainerEngineFlag(cmd *cobra.Command, _ []string) error {
	flag := cmd.Flag("container-engine")
	if flag != nil && flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
		return fmt.Errorf("--container-engine requires docker or podman")
	}
	return nil
}

// Keep retired spellings parseable but absent from help and completion.
// Returning directly avoids loading config for commands with no surviving runtime.
func retiredCommand(name, replacement string) *cobra.Command {
	notice := func(cmd *cobra.Command) string {
		return fmt.Sprintf("%s is retired; use %s", cmd.CommandPath(), replacement)
	}
	cmd := &cobra.Command{Use: name, Hidden: true,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return errors.New(notice(cmd))
		}}
	cmd.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		fmt.Fprintln(cmd.OutOrStdout(), notice(cmd))
	})
	return cmd
}

func appRun(state *commandState, fn func(*app.App) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a, err := state.operationApplication(cmd)
		if err != nil {
			return err
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
		fmt.Fprintf(cmd.OutOrStdout(), "kmx %s\n  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md\n  orka     %s\n  model    %s\n",
			version.Resolve(info, ok), app.OrkaVersion, config.DefaultModel)
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
