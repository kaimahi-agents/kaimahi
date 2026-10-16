package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	policy "github.com/agent-primitives/suite/policy"
	"github.com/spf13/cobra"
)

const maxPolicyFileBytes = 4 << 20

func readPolicyFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPolicyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPolicyFileBytes {
		return nil, fmt.Errorf("policy file exceeds %d bytes", maxPolicyFileBytes)
	}
	return data, nil
}

func newSuitePolicyCommand() *cobra.Command {
	var adminPath string
	cmd := &cobra.Command{Use: "policy FILE", Short: "Resolve a portable AgentSuite policy", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) (err error) {
		defer func() {
			if err != nil {
				err = &suiteDiagnosticError{err}
			}
		}()
		if cmd.Flags().Changed("administrative-policy") && adminPath == "" {
			return errors.New("--administrative-policy cannot be empty")
		}
		raw, err := readPolicyFile(args[0])
		if err != nil {
			return err
		}
		in, err := policy.DecodeSuitePolicy(raw)
		if err != nil {
			return err
		}
		admin := policy.AdministrativePolicy{}
		if adminPath != "" {
			b, e := readPolicyFile(adminPath)
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
