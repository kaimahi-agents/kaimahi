package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func newRunProfileCommand(state *commandState) *cobra.Command {
	group := &cobra.Command{
		Use: "run-profile", Short: "Export canonical agent-file content offline for a separate runtime",
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	group.AddCommand(&cobra.Command{
		Use: "export FILE", Short: "Export a frozen source-file run profile as JSON on stdout",
		Long: "Export one canonical agent file offline through the same parser as file Run/Test. The deterministic JSON on stdout contains authored instructions, model identity, ordered examples/assertions and SHA-256 hashes. Every example has targetStatus not_run; no configuration, credentials, model or runtime is accessed.\n\n" +
			"This content-bearing source projection is not a package, evaluation receipt, provider attestation or deployment authorization. Authored model and judge identities are preserved without overrides. A run profile is not an AgentSuite runtime profile.\n\n" +
			"Keep stdout private: it includes instructions, inputs and rubrics. Shell redirection is caller-owned, may truncate before validation, and has no kmx privacy or exclusive-create guarantee. No --out writer is provided.",
		Example: "  kmx run-profile export ./release-writer.agent.md",
		Args:    cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if flag := cmd.Flag("context"); state.contextFlag != "" || flag != nil && flag.Changed {
				return errors.New("run-profile export does not use --context")
			}
			if flag := cmd.Flag("container-engine"); flag != nil && flag.Changed {
				return errors.New("run-profile export does not use --container-engine")
			}
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{"md"}, cobra.ShellCompDirectiveFilterFileExt
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := agentfile.ExportRunProfile(args[0])
			if err != nil {
				return err
			}
			// Marshal before output; a failed writer can still receive partial bytes.
			// Do not disclose its possibly content-bearing error in diagnostics.
			if n, err := cmd.OutOrStdout().Write(data); err != nil || n != len(data) {
				return errors.New("write run profile: output incomplete")
			}
			return nil
		},
	})
	return group
}
