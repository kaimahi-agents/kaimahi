package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func newAgentVerifyCommand() *cobra.Command {
	var opt app.VerifyAgentSessionsOptions
	cmd := &cobra.Command{
		Use:   "verify <sessions-receipt.json>",
		Short: "Replay a sessions receipt with zero live model calls",
		Long: "Verify each receipt-bound journal prefix using kmx's pinned built-in chat harness.\n" +
			"Requires an explicit --sessions destination matching the receipt's normalized address.\n" +
			"Reads journals without executing a new session or contacting a model endpoint.\n" +
			"Writes a private verify-<receipt-basename>.json sibling; never changes evaluation verdicts.\n" +
			"Proves local reference equivalence, not the original host implementation or version.\n" +
			"Non-loopback connections require verified TLS; --sessions-ca adds a private CA.\n" +
			"Sessions verification does not satisfy Orka lift or status gates.",
		Args: usageArgs(1, 1, "kmx agent verify <sessions-receipt.json> --sessions <host:port> [--sessions-ca <pem>] [--timeout 5m]"),
	}
	cmd.Flags().StringVar(&opt.Sessions, "sessions", "", "operator-selected agentsessions host:port matching the receipt (required)")
	cmd.Flags().StringVar(&opt.SessionsCA, "sessions-ca", "", "PEM CA file for verified TLS to the sessions host")
	cmd.Flags().DurationVar(&opt.Timeout, "timeout", 5*time.Minute, "time for all receipt verification (10s–9m)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(opt.Sessions) == "" {
			return fmt.Errorf("--sessions is required")
		}
		if cmd.Flags().Changed("sessions-ca") && strings.TrimSpace(opt.SessionsCA) == "" {
			return fmt.Errorf("--sessions-ca requires a non-empty value")
		}
		if opt.Timeout < 10*time.Second || opt.Timeout > 9*time.Minute {
			return fmt.Errorf("--timeout must be between 10s and 9m0s")
		}
		opt.ReceiptPath = cmd.Flags().Arg(0)
		a := &app.App{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Run: &run.Runner{Context: cmd.Context()}}
		return a.VerifyAgentSessions(opt)
	}
	return cmd
}
