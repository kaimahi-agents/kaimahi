package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	goruntime "runtime"
	"strings"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
	"github.com/spf13/cobra"
)

type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }

func newMCPCommand(state *commandState) *cobra.Command {
	var root, provider, endpoint, squadProgram, squadRelease string
	var clearHistory bool
	cmd := &cobra.Command{Use: "mcp [--root DIRECTORY]", Short: "Serve Create/Edit/Run/Test over local stdio MCP", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" || cmd.Flag("container-engine").Changed {
				return fmt.Errorf("MCP refuses cluster/container selectors")
			}
			for _, name := range []string{"root", "provider", "endpoint", "squad-program", "squad-release"} {
				flag := cmd.Flag(name)
				if flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("MCP requires nonempty --%s", name)
				}
			}
			if root == "" && (clearHistory || squadProgram != "" || squadRelease != "") {
				return fmt.Errorf("required flag(s) \"root\" not set")
			}
			if clearHistory {
				// Maintenance only: no route, Squad setup, stdin or server.
				return clearMCPHistory(cmd, root)
			}
			var route *modelaccess.Route
			if provider != "" || endpoint != "" {
				selected := fileModelRoute(provider, endpoint)
				selected.Model = "validation"
				if selected.Validate() != nil {
					return fmt.Errorf("invalid trusted MCP provider/endpoint; no automatic selection")
				}
				selected.Model = ""
				route = &selected
			}
			files, err := openMCPFiles(root)
			if err != nil {
				return err
			}
			defer files.Close()
			if state.deps.stdin == nil {
				return fmt.Errorf("MCP requires protocol stdin")
			}
			application := &app.App{FileRunner: state.deps.fileRunner}
			operations := kmxmcp.Operations(files, application, route)
			var squad *kmxmcp.SquadCreator
			if squadProgram != "" || squadRelease != "" {
				if squadProgram == "" || squadRelease == "" {
					return fmt.Errorf("MCP native Squad requires explicit program and qualified release")
				}
				squad, err = kmxmcp.OpenSquadSetup(root, kmxmcp.SquadProgram{Path: squadProgram, Release: squadRelease, GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH})
				if err != nil {
					return err
				}
				defer squad.Close()
				operations = kmxmcp.WithSquadOperations(operations, squad, application, route)
			}
			info, ok := state.deps.buildInfo()
			server, err := kmxmcp.NewServer(version.Resolve(info, ok).Version, operations)
			if err != nil {
				return err
			}
			kmxmcp.AddAgentResource(server, files)
			kmxmcp.AddAgentVersionResource(server, files)
			if squad != nil {
				kmxmcp.AddSquadResource(server, squad)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := kmxmcp.Serve(ctx, server, state.deps.stdin, mcpOutput{cmd.OutOrStdout()}); err != nil {
				if ctx.Err() != nil || errors.Is(err, io.EOF) {
					return nil
				}
				return fmt.Errorf("MCP stdio session failed")
			}
			return nil
		}}
	cmd.Flags().StringVar(&root, "root", "", "optional absolute existing operation root for legacy clients; otherwise agent paths resolve against cwd")
	cmd.Flags().StringVar(&provider, "provider", "", "trusted startup model provider, including copilot (not selectable through tools)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "trusted startup model endpoint (fixed for Copilot; never read from charter text)")
	cmd.Flags().StringVar(&squadProgram, "squad-program", "", "trusted absolute qualified standalone Squad launcher; enables native targets, never installs")
	cmd.Flags().StringVar(&squadRelease, "squad-release", "", "explicit qualified native Squad release (1.1.0); requires --squad-program")
	cmd.Flags().BoolVar(&clearHistory, "clear-history", false, "clear retained source history for --root, then exit; does not start MCP or touch current files")
	for _, name := range []string{"provider", "endpoint", "squad-program", "squad-release"} {
		cmd.MarkFlagsMutuallyExclusive("clear-history", name)
	}
	return cmd
}

// openMCPFiles resolves the history directory lazily, so startup and help
// never read config or create state.
func openMCPFiles(root string) (*kmxmcp.Files, error) {
	return kmxmcp.OpenFilesWithHistory(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err }, config.LocalAgentVersionDir)
}

func clearMCPHistory(cmd *cobra.Command, root string) error {
	files, err := openMCPFiles(root)
	if err != nil {
		return err
	}
	defer files.Close()
	if err := files.ClearHistory(cmd.Context()); err != nil {
		var refused *kmxmcp.Error
		if errors.As(err, &refused) {
			return err
		}
		return fmt.Errorf("cannot clear history safely; no current files were changed")
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Cleared retained source history for the selected root."); err != nil {
		return fmt.Errorf("source history was cleared, but its confirmation could not be written")
	}
	return nil
}
