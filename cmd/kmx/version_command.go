package main

import (
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
	"github.com/spf13/cobra"
)

type versionCapability struct {
	Runner        string `json:"runner,omitempty"`
	Model         string `json:"model,omitempty"`
	Operation     string `json:"operation"`
	Output        string `json:"output"`
	SchemaVersion int    `json:"schemaVersion"`
}

type versionDocument struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Kind          string              `json:"kind"`
	KmxVersion    string              `json:"kmxVersion"`
	OrkaVersion   string              `json:"orkaVersion"`
	Capabilities  []versionCapability `json:"capabilities"`
}

func newVersionCommand(state *commandState) *cobra.Command {
	output := "text"
	cmd := &cobra.Command{
		Use: "version", Short: "Show this build and the component versions it targets",
		Example: "  kmx version\n  kmx version -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("version output must be text or json")
			}
			info, ok := state.deps.buildInfo()
			build := version.Resolve(info, ok)
			if output == "text" {
				fmt.Fprintf(cmd.OutOrStdout(), "kmx %s\n  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md\n  orka     %s\n  model    %s\n",
					build, app.OrkaVersion, config.DefaultModel)
				return nil
			}
			if state.machine == nil {
				state.machine = newMachineOutput(cmd.OutOrStdout(), "version")
			}
			capabilities := []versionCapability{{Operation: "version", Output: "json", SchemaVersion: 1}}
			if state.deps.goos != "windows" {
				for _, operation := range []string{"cluster.up", "cluster.orka.install"} {
					capabilities = append(capabilities, versionCapability{Operation: operation, Output: "jsonl", SchemaVersion: 1})
				}
			}
			for _, runner := range []string{"embedded", "copilotd"} {
				if state.deps.goos == "windows" && runner == "copilotd" {
					continue
				}
				c := versionCapability{Operation: "file.test", Output: "jsonl", SchemaVersion: 1, Runner: runner}
				if state.deps.goos == "windows" {
					c.Model = "fake"
				}
				capabilities = append(capabilities, c)
			}
			if state.deps.goos != "windows" {
				capabilities = append(capabilities, versionCapability{Operation: "cluster.orka.tools", Output: "json", SchemaVersion: 1})
			}
			err := state.machine.document(versionDocument{
				SchemaVersion: 1, Kind: "kmx.version", KmxVersion: build.Version,
				OrkaVersion:  app.OrkaVersion,
				Capabilities: capabilities,
			})
			if err != nil {
				return &machineCommandError{cause: err, safe: app.NewMachineError("output_failed", "")}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output: text or json")
	_ = cmd.RegisterFlagCompletionFunc("output", staticCompletion([]string{"text", "json"}))
	return cmd
}
