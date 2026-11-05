package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/spf13/cobra"
)

type discoveryMachineResult struct {
	app.MachineResult
	Discovery app.DiscoveryReport `json:"discovery"`
}

func (m *machineOutput) discoveryResult(report app.DiscoveryReport, exit int, safe *app.MachineError, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeLocked(discoveryMachineResult{app.MachineResult{MachineEnvelope: m.envelope("kmx.result"), Outcome: outcome, Verdict: "not_applicable", ExitCode: exit, Error: safe}, report}, true)
}

func newClusterDiscoveryCommand(state *commandState) *cobra.Command {
	output, namespace := "text", ""
	cmd := &cobra.Command{Use: "discover", Short: "Discover context labels, namespaces or an explicit target without changing it",
		Long:    "Without --context, read only local kubeconfig context labels. With an explicit\ncontext, list namespace names and UIDs using bounded GETs. Add --namespace to\nobserve target identity, native runtime readiness, declared Providers/models and\nTool inventory. No runtime, credentials, preferences or other resources are created.\nPartial reads exit 1, never prove absence or execution permission. Existing kubectl\ncredential helpers may run; input is detached and no prompt is supported.",
		Example: "  kmx cluster discover\n  kmx cluster discover --context dev -o jsonl", Args: cobra.NoArgs}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output: text or jsonl")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Observe this target namespace (requires explicit --context)")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "jsonl"}))
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if output != "text" && output != "jsonl" {
			safe := app.NewMachineError("invalid_arguments", "preflight")
			return &machineCommandError{cause: safe, safe: safe}
		}
		signals, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithCancel(signals)
		defer cancel()
		m := newMachineOutput(cmd.OutOrStdout(), "cluster.discover")
		m.cancel = cancel
		// Deliberately do not acquire a configured App: doing so would load saved
		// selectors, KUBE_CTX and mutable state before discovery has selected one.
		a := &app.App{Run: &run.Runner{Context: ctx}}
		var report app.DiscoveryReport
		var err error
		if cmd.Flags().Changed("namespace") && namespace == "" {
			report = app.DiscoveryReport{Scope: "target", Checks: []app.DiscoveryCheck{{Scope: "target", State: "unknown", Reason: "malformed"}}, Rows: []app.DiscoveryRow{}}
			err = app.NewMachineError("invalid_arguments", "preflight")
		} else {
			report, err = a.Discover(ctx, app.DiscoveryOptions{Context: state.contextFlag, Namespace: namespace})
		}
		code, outcome := 0, "complete"
		var safe *app.MachineError
		if err != nil {
			code, outcome = 1, "failed"
			safe = app.DiscoveryError(err)
			if errors.Is(err, context.Canceled) {
				outcome = "cancelled"
			}
		}
		var writeErr error
		if output == "jsonl" {
			writeErr = m.discoveryResult(report, code, safe, outcome)
		} else {
			_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "Discovery %s (complete: %t)\n", report.Scope, report.Complete)
			for _, row := range report.Rows {
				if writeErr != nil {
					break
				}
				if row.Current != nil {
					_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s\tcurrent=%t\n", row.Context, *row.Current)
				} else {
					_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", row.Context, row.Namespace, row.UID)
				}
			}
			if t := report.Target; t != nil && writeErr == nil {
				_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tcluster=%s\tnamespace=%s\n", t.Context, t.Namespace, t.ClusterUID, t.NamespaceUID)
				if t.Runtime != nil && writeErr == nil {
					_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "Runtime\t%s\t%s\t%s\n", t.Runtime.Version, t.Runtime.State, t.Runtime.Reason)
				}
				for _, p := range t.Providers {
					if writeErr != nil {
						break
					}
					_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "Provider\t%s\t%s\tmodel=%s\tReady=%s\n", p.Name, p.Type, p.Model, p.ConditionState)
				}
				if t.Tools != nil {
					for _, tool := range t.Tools.Tools {
						if writeErr != nil {
							break
						}
						_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "Tool\t%s\t%s\t%s\n", tool.Name, tool.Availability, tool.Reason)
					}
				}
			}
		}
		if writeErr != nil {
			cancel()
			return &machineCommandError{cause: writeErr, safe: app.NewMachineError("output_failed", "")}
		}
		if err != nil {
			return &machineCommandError{cause: err, safe: safe}
		}
		return nil
	}
	return cmd
}
