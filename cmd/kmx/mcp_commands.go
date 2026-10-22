package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
	"github.com/spf13/cobra"
)

type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }

func newMCPCommand(state *commandState) *cobra.Command {
	var root, provider, endpoint string
	cmd := &cobra.Command{Use: "mcp --root DIRECTORY", Short: "Serve Create/Edit/Run/Test over local stdio MCP", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" || cmd.Flag("container-engine").Changed {
				return fmt.Errorf("MCP refuses cluster/container selectors")
			}
			for _, name := range []string{"root", "provider", "endpoint"} {
				flag := cmd.Flag(name)
				if flag.Changed && strings.TrimSpace(flag.Value.String()) == "" {
					return fmt.Errorf("MCP requires nonempty --%s", name)
				}
			}
			var route *modelaccess.Route
			if provider != "" || endpoint != "" {
				selected := modelaccess.Route{Provider: provider, Endpoint: endpoint, Auth: "none", Model: "validation"}
				if provider == "copilot" {
					selected.Auth = "copilot"
				}
				if selected.Validate() != nil {
					return fmt.Errorf("invalid trusted MCP provider/endpoint; no automatic selection")
				}
				selected.Model = ""
				route = &selected
			}
			files, err := kmxmcp.OpenFiles(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err })
			if err != nil {
				return err
			}
			defer files.Close()
			if state.deps.stdin == nil {
				return fmt.Errorf("MCP requires protocol stdin")
			}
			info, ok := state.deps.buildInfo()
			server, err := kmxmcp.NewServer(version.Resolve(info, ok).Version, kmxmcp.Operations(files, &app.App{FileRunner: state.deps.fileRunner}, route))
			if err != nil {
				return err
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
	cmd.Flags().StringVar(&root, "root", "", "absolute existing operation root; required, never defaults to cwd")
	cmd.Flags().StringVar(&provider, "provider", "", "trusted startup model provider (not selectable through tools)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "trusted startup model endpoint (never read from charter text)")
	_ = cmd.MarkFlagRequired("root")
	return cmd
}
