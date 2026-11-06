package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newLocalAgentListCommand(state *commandState) *cobra.Command {
	var all bool
	var output, namespace string
	cmd := &cobra.Command{
		Use: "list", Short: "List active local agents or Orka agents in an explicit context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.contextFlag != "" {
				if output != "table" && output != "json" && output != "yaml" {
					return fmt.Errorf("remote list output must be table, json or yaml")
				}
				if all {
					return fmt.Errorf("--all applies only to local agents")
				}
				a, err := state.operationApplication(cmd)
				if err != nil {
					return err
				}
				return a.ListAgents(output, namespace)
			}
			if output != "table" && output != "json" {
				return fmt.Errorf("local list output must be table or json")
			}
			if cmd.Flags().Changed("namespace") || cmd.Flags().Changed("container-engine") {
				return fmt.Errorf("local list does not use cluster selectors; add an explicit --context for Orka")
			}
			rows, err := state.deps.readLocalAgents(all)
			if err != nil {
				return err
			}
			if output == "json" {
				type envelope struct {
					SchemaVersion int    `json:"schemaVersion"`
					Kind          string `json:"kind"`
					Scope         string `json:"scope"`
					Agents        any    `json:"agents"`
				}
				type row struct {
					ID               string     `json:"id"`
					AgentID          string     `json:"agentId"`
					InstanceID       string     `json:"instanceId"`
					Generation       uint64     `json:"generation"`
					Name             string     `json:"name"`
					State            string     `json:"state"`
					Provider         string     `json:"provider,omitempty"`
					Model            string     `json:"model,omitempty"`
					SnapshotRevision string     `json:"snapshotRevision"`
					Started          time.Time  `json:"started"`
					Stopped          *time.Time `json:"stopped,omitempty"`
					Ready            bool       `json:"ready"`
					Sessions         int        `json:"sessions"`
					LastActivity     *time.Time `json:"lastActivity,omitempty"`
					LastError        string     `json:"lastError,omitempty"`
				}
				projected := make([]row, 0, len(rows))
				for _, item := range rows {
					projected = append(projected, row{ID: item.ID, AgentID: item.ID, InstanceID: item.InstanceID, Generation: item.Sequence, Name: item.Name, State: item.State, Provider: item.Provider, Model: item.Model, SnapshotRevision: item.SnapshotRevision, Started: item.Started, Stopped: item.Stopped, Ready: item.Ready, Sessions: item.Sessions, LastActivity: item.LastActivity, LastError: item.LastError})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope{SchemaVersion: 1, Kind: "kmx.local-agent-inventory", Scope: "local", Agents: projected})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "LOCAL AGENTS")
			fmt.Fprintln(cmd.OutOrStdout(), "AGENT         INSTANCE       GEN NAME                 STATE     READY SESSIONS MODEL                    AGE")
			for _, row := range rows {
				age := time.Since(row.Started).Round(time.Second)
				model := row.Model
				if model == "" {
					model = "default"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-13s %-14s %-3d %-20s %-9s %-5t %-8d %-24s %s\n", strings.TrimPrefix(row.ID, "md-")[:12], strings.TrimPrefix(row.InstanceID, "li-")[:12], row.Sequence, row.Name, row.State, row.Ready, row.Sessions, model, age)
				if row.LastActivity != nil {
					fmt.Fprintln(cmd.OutOrStdout(), "  Last activity:", row.LastActivity.Format(time.RFC3339))
				}
				if row.LastError != "" {
					fmt.Fprintln(cmd.OutOrStdout(), "  Warning:", row.LastError)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include stopped local agents")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output: table|json")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Orka namespace (requires explicit --context)")
	return cmd
}

func newLocalAgentStopCommand(state *commandState) *cobra.Command {
	return &cobra.Command{Use: "stop NAME_OR_ID", Short: "Stop a resident local agent", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if state.contextFlag != "" || cmd.Flags().Changed("container-engine") {
			return fmt.Errorf("stop selects a local agent; omit cluster selectors")
		}
		if err := state.deps.stopLocalAgent(cmd.Context(), args[0]); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Stopped", args[0])
		return err
	}}
}
