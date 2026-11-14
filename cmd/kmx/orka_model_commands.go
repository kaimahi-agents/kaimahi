package main

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newOrkaModelCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{Use: "model", Short: "Connect or disconnect an owned Orka model route", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	for _, verb := range []string{"connect", "disconnect"} {
		group.AddCommand(newOrkaModelLeaf(state, verb))
	}
	return group
}
func newOrkaModelLeaf(state *commandState, verb string) *cobra.Command {
	var connect app.OrkaModelConnectOptions
	var connection string
	var result app.OrkaModelResult
	long := "Use a guest-memory Copilot token or API key in the owned Linux VM.\nNo runtime installation, sign-in flow, model inference or resource adoption.\nAn uncertain operation retains its connection ID for explicit recovery."
	if verb == "disconnect" {
		long = "Stop only the retained model connection and owned resources in the Linux VM.\nNo credential reads, runtime removal, sign-in flow, model inference or resource adoption.\nAn uncertain operation retains its connection ID for explicit recovery."
	}
	cmd := &cobra.Command{Use: verb, Short: map[string]string{"connect": "Connect a model route through the owned gateway without inference", "disconnect": "Stop only the retained model connection and owned resources"}[verb], Long: long, Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fmt.Errorf("model %s accepts no positional arguments", verb)
		}
		if connect.Namespace != app.OrkaNamespace || strings.TrimSpace(connect.Provider) == "" {
			return fmt.Errorf("model %s requires --namespace orka-system and --provider NAME", verb)
		}
		if verb == "connect" {
			id, err := uuid.Parse(connect.CredentialGeneration)
			if !(connect.Route == "copilot" && connect.Vendor == "" || connect.Route == "key" && (connect.Vendor == "openai" || connect.Vendor == "anthropic")) || strings.TrimSpace(connect.Model) == "" || err != nil || id.String() != connect.CredentialGeneration || id.Version() != 4 || id.Variant() != uuid.RFC4122 {
				return fmt.Errorf("model connect requires --route copilot or --route key --vendor openai|anthropic, --model ID and --credential-generation UUIDv4")
			}
		} else if id, err := uuid.Parse(connection); err != nil || id.String() != connection || id.Version() != 4 || id.Variant() != uuid.RFC4122 {
			return fmt.Errorf("model disconnect requires --connection UUIDv4")
		}
		return nil
	}}
	cmd.Flags().StringVarP(&connect.Namespace, "namespace", "n", "", "exact runtime namespace (required: orka-system)")
	cmd.Flags().StringVar(&connect.Provider, "provider", "", "owned native Provider name (required)")
	if verb == "connect" {
		cmd.Flags().StringVar(&connect.Route, "route", "", "model route (required: copilot or key)")
		cmd.Flags().StringVar(&connect.Vendor, "vendor", "", "API key company (key route only: openai or anthropic)")
		cmd.Flags().StringVar(&connect.Model, "model", "", "exact advertised model ID (required)")
		cmd.Flags().StringVar(&connect.CredentialGeneration, "credential-generation", "", "endpoint-issued credential generation UUIDv4 (required)")
		cmd.Example = "  kmx cluster orka model connect --namespace orka-system --route copilot \\\n    --provider copilot --model gpt-4.1 --credential-generation UUID\n" +
			"  kmx cluster orka model connect --namespace orka-system --route key \\\n    --vendor openai --provider workspace --model gpt-4.1 --credential-generation UUID -o jsonl"
	} else {
		cmd.Flags().StringVar(&connection, "connection", "", "retained connection UUIDv4 (required)")
		cmd.Example = "  kmx cluster orka model disconnect --namespace orka-system \\\n    --provider copilot --connection UUID\n" +
			"  kmx cluster orka model disconnect --namespace orka-system \\\n    --provider copilot --connection UUID -o jsonl"
	}
	bindProvisioningOutput(cmd, state, "cluster.orka.model."+verb, func() provisioningSelection {
		stages := []string{"gateway", "provider", "model"}
		if verb == "disconnect" {
			stages = []string{"gateway", "provider", "finalize"}
		}
		return provisioningSelection{mode: "apply", stages: stages, deferPreflight: true, operationID: &connect.OperationID, model: &result}
	}, func(a *app.App) error {
		var err error
		if verb == "connect" {
			result, err = a.ConnectOrkaModel(connect)
		} else {
			result, err = a.DisconnectOrkaModel(app.OrkaModelDisconnectOptions{OperationID: connect.OperationID, Namespace: connect.Namespace, Provider: connect.Provider, Connection: connection})
		}
		if result.Connection != nil {
			if _, e := fmt.Fprintf(a.Out, "Model %s.\nConnection: %s\n", result.Connection.State, result.Connection.ID); e != nil {
				return &machineCommandError{cause: e, safe: app.NewMachineError("output_failed", "")}
			}
		}
		return err
	})
	return cmd
}
