package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/spf13/cobra"
)

func newOrkaToolsCommand(state *commandState) *cobra.Command {
	namespace, output := "", "text"
	cmd := &cobra.Command{
		Use:     "tools --context CONTEXT --namespace NAMESPACE",
		Short:   "Discover native Orka tool availability without invoking tools",
		Long:    "Read namespace-local HTTP Tool resources and a version-qualified pinned builtin catalog.\nAvailability is an observation, not an endpoint call or evaluation proof.\nNo TokenRequest, Secret read, provisioning or tool execution.",
		Example: "  kmx cluster orka tools --context kind-dev --namespace orka-system\n  kmx cluster orka tools --context team --namespace agents -o json",
		Args:    cobra.NoArgs,
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "explicit Agent/Tool namespace")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var machine *machineOutput
		if output == "json" {
			machine = newMachineOutput(cmd.OutOrStdout(), "cluster.orka.tools")
		}
		finish := func(inventory *app.OrkaToolInventory, cause error, code string) error {
			var safe *app.MachineError
			outcome, exit := "complete", 0
			if cause != nil {
				outcome, exit = "failed", 1
				safe = app.NewMachineError(code, "")
			}
			if machine != nil {
				record := struct {
					app.MachineResult
					Inventory *app.OrkaToolInventory `json:"inventory,omitempty"`
				}{
					MachineResult: app.MachineResult{MachineEnvelope: machine.envelope("kmx.result"), Outcome: outcome, Verdict: "not_applicable", ExitCode: exit, Error: safe}, Inventory: inventory,
				}
				if err := machine.document(record); err != nil {
					return &machineCommandError{cause: err, safe: app.NewMachineError("output_failed", "")}
				}
			} else if inventory != nil {
				for _, row := range inventory.Tools {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", row.Kind, row.Name, row.Availability, row.Reason, row.Source); err != nil {
						return &machineCommandError{cause: err, safe: app.NewMachineError("output_failed", "")}
					}
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "complete=%t reason=%s (availability is not execution proof)\n", inventory.Complete, inventory.Reason); err != nil {
					return &machineCommandError{cause: err, safe: app.NewMachineError("output_failed", "")}
				}
			}
			if cause != nil {
				return &machineCommandError{cause: cause, safe: safe}
			}
			return nil
		}
		if output != "text" && output != "json" || state.contextFlag == "" || !cmd.Flags().Changed("namespace") || scaffold.ValidateNamespace(namespace) != nil {
			return finish(nil, fmt.Errorf("tools requires explicit --context, --namespace and text/json output"), "invalid_arguments")
		}
		if state.deps.goos == "windows" {
			return finish(nil, fmt.Errorf("unsupported"), "unsupported")
		}
		a, err := state.operationApplication(cmd)
		if err != nil {
			return finish(nil, err, "configuration_invalid")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		inventory, err := a.ReadOrkaTools(ctx, app.OrkaToolOptions{Namespace: namespace})
		return finish(&inventory, err, "operation_failed")
	}
	return cmd
}
