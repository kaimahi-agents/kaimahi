package main

import (
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/spf13/cobra"
)

func newTargetsCommand(state *commandState) *cobra.Command {
	opt := app.TargetsOptions{}
	cmd := &cobra.Command{
		Use: "targets", Short: "Show compiled runtime support and optional read-only installation probes",
		Long: "Show compiled capabilities and exact qualification evidence, not research candidates.\n" +
			"The default view is offline. --detect reads Orka controllers in orka-system using\n" +
			"the explicit or saved Kubernetes context. --sessions independently probes only\n" +
			"the supplied Sessions API, not its harness or registry. No runtime is selected,\n" +
			"installed or deployed. Unreadable probes produce a complete report and exit nonzero.",
		Args: usageArgs(0, 0, "kmx targets [--detect] [--sessions <host:port>] [-o table|json]"),
	}
	cmd.Flags().StringVarP(&opt.Output, "output", "o", "table", "output: table|json")
	cmd.Flags().BoolVar(&opt.Detect, "detect", false, "read Orka controller installation evidence in the selected Kubernetes context")
	cmd.Flags().StringVar(&opt.Sessions, "sessions", "", "read this agentsessions Sessions API (host:port), independently of Kubernetes")
	cmd.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified TLS to the Sessions host")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"table", "json"}))
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if opt.Output != "table" && opt.Output != "json" {
			return fmt.Errorf("targets output must be table or json")
		}
		for _, name := range []string{"sessions", "sessions-ca"} {
			if cmd.Flags().Changed(name) {
				value, _ := cmd.Flags().GetString(name)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s requires a non-empty value", name)
				}
			}
		}
		if opt.SessionsCA != "" && opt.Sessions == "" {
			return fmt.Errorf("--sessions-ca requires --sessions")
		}
		if opt.Sessions != "" {
			if _, err := agentsessions.NormalizeAddress(opt.Sessions); err != nil {
				return err
			}
		}
		a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Run: &run.Runner{Context: cmd.Context()}}
		if opt.Detect {
			configured, err := state.application()
			if err == nil {
				copyApp := *configured
				runner := *configured.Run
				runner.Context = cmd.Context()
				copyApp.Run = &runner
				a = &copyApp
			}
			// Configuration failure becomes an unreadable Kubernetes observation,
			// not an early return that hides the independent Sessions observation.
		}
		return a.Targets(opt)
	}
	return cmd
}
