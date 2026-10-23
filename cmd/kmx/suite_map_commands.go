package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/spf13/cobra"
)

func newSuiteMapCommand(state *commandState) *cobra.Command {
	var tests, destination, model, name, output string
	cmd := &cobra.Command{Use: "map FILE", Short: "Map an md definition and tests to a native Orka suite workspace", Long: "Explicit offline mapping of the saved working copy into a new one-member Orka workspace. Requires an exact target model and complete text-only cases. Tools and mocks refuse. Existing workspace files are never overwritten. Copilot results are not copied: evaluate this workspace on Orka before push/deploy.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return errors.New("suite map is offline; omit operational selectors")
		}
		if output != "text" && output != "json" {
			return errors.New("output must be text, json or yaml")
		}
		for _, flag := range []string{"tests", "name"} {
			v, _ := cmd.Flags().GetString(flag)
			if cmd.Flags().Changed(flag) && strings.TrimSpace(v) == "" {
				return fmt.Errorf("--%s must not be blank", flag)
			}
		}
		report, err := app.MapMarkdownSuite(cmd.Context(), args[0], tests, destination, model, name)
		if err != nil {
			return err
		}
		if output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Mapped %s → %s\nDefinition: %s\nTests: %s\nRuntime: orka · model: %s · cases: %d\n\nNative evaluation required; local Copilot results do not authorize deployment.\nNext: %s test --workspace %s --to-context <evaluation-context> --inference provider:<name> --out <new-private-receipt.json>\n", report.Source, report.Workspace, report.AgentDigest, report.TestsDigest, report.DeploymentModel, report.Cases, state.executable(), quoteShell(report.Workspace))
		return err
	}}
	cmd.Flags().StringVar(&destination, "workspace", "", "destination native workspace directory (required)")
	cmd.Flags().StringVar(&model, "model", "", "exact deployment model (required; never inferred from Copilot auto/default)")
	cmd.Flags().StringVar(&tests, "tests", "", "explicit companion tests outside the conventional location")
	cmd.Flags().StringVar(&name, "name", "", "native member name override (default: agent name)")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output: text|json|yaml")
	return cmd
}
