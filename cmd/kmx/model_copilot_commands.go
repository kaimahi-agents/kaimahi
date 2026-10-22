package main

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/spf13/cobra"
)

type copilotAccess interface {
	Login(context.Context, func(copilot.DevicePrompt) error) error
	LoginGH(context.Context) error
	Logout() error
	List(context.Context) ([]modelaccess.Route, error)
}

func (s *commandState) copilot() copilotAccess {
	if s.deps.newCopilot != nil {
		return s.deps.newCopilot()
	}
	return copilot.New(nil)
}
func addCopilotCommands(group *cobra.Command, state *commandState) {
	useGH := false
	login := &cobra.Command{Use: "login", Short: "Sign in for direct Copilot model access using GitHub device flow", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		access := state.copilot()
		var err error
		if useGH {
			err = access.LoginGH(cmd.Context())
		} else {
			err = access.Login(cmd.Context(), func(prompt copilot.DevicePrompt) error {
				_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Open %s and enter code %s\n", prompt.VerificationURI, prompt.UserCode)
				return err
			})
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.ErrOrStderr(), "Copilot credential saved in kmx's OS keychain item.")
		return err
	}}
	login.Flags().BoolVar(&useGH, "gh", false, "explicitly copy the existing gh auth token for github.com into kmx's OS keychain item")
	logout := &cobra.Command{Use: "logout", Short: "Delete only kmx's Copilot credential and clear its memory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := state.copilot().Logout(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), "kmx Copilot credential removed; gh and editor sign-in unchanged.")
		return err
	}}
	group.AddCommand(login, logout)
}
func listCopilotModels(cmd *cobra.Command, state *commandState, format string) error {
	routes, err := state.copilot().List(cmd.Context())
	if err != nil {
		return err
	}
	if routes == nil {
		routes = []modelaccess.Route{}
	}
	if format == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(routes)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tMODEL\tENDPOINT\tAUTH")
	for _, r := range routes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Provider, r.Model, r.Endpoint, r.Auth)
	}
	return w.Flush()
}
