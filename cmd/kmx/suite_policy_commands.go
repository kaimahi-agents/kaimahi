package main

import (
	"fmt"
	"os"

	policy "github.com/agent-primitives/suite/policy"
	"github.com/spf13/cobra"
)

func newSuitePolicyCommand() *cobra.Command {
	var adminPath string
	cmd := &cobra.Command{Use: "policy FILE", Short: "Resolve a portable AgentSuite policy", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		in, err := policy.DecodeSuitePolicy(raw)
		if err != nil {
			return err
		}
		admin := policy.AdministrativePolicy{}
		if adminPath != "" {
			b, e := os.ReadFile(adminPath)
			if e != nil {
				return e
			}
			admin, e = policy.DecodeAdministrativePolicy(b)
			if e != nil {
				return e
			}
		}
		effective, err := policy.Resolve(in, admin)
		if err != nil {
			return err
		}
		payload, err := policy.Payload(effective)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n", payload.PolicyBytes, payload.PolicyDigest)
		return err
	}}
	cmd.Flags().StringVar(&adminPath, "administrative-policy", "", "optional administrative policy JSON")
	return cmd
}
