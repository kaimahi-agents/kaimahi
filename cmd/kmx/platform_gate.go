package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

var errWindowsOperationUnsupported = errors.New("this operation is not available in the guarded native Windows day-zero path")

const platformAnnotation = "kaimahi.dev/platform-gate"

// installPlatformGate refuses deferred Windows operations before configuration,
// state, subprocess, network, or ordinary output effects. Required help,
// completion and compatibility notices retain their existing output contracts.
func installPlatformGate(root *cobra.Command, state *commandState) {
	if state.deps.goos != "windows" {
		return
	}
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[platformAnnotation] = "windows-guarded"
	var wrap func(*cobra.Command)
	wrap = func(cmd *cobra.Command) {
		if cmd != root {
			args := cmd.Args
			cmd.Args = func(cmd *cobra.Command, values []string) error {
				if windowsCommandAllowed(cmd, values, state) {
					if args != nil {
						return args(cmd, values)
					}
					return nil
				}
				if strings.HasPrefix(strings.TrimPrefix(cmd.CommandPath(), "kmx "), "eval") {
					return errors.Join(state.noticeLegacyEval(cmd), errWindowsOperationUnsupported)
				}
				if strings.TrimPrefix(cmd.CommandPath(), "kmx ") == "lift" {
					_, noticeErr := fmt.Fprintln(cmd.ErrOrStderr(), "kmx lift is deprecated; use kmx package deploy (same arguments).")
					return errors.Join(noticeErr, errWindowsOperationUnsupported)
				}
				return errWindowsOperationUnsupported
			}
			cmd.ValidArgsFunction = cobra.NoFileCompletions
		}
		for _, child := range cmd.Commands() {
			wrap(child)
		}
	}
	wrap(root)
}

func windowsCommandAllowed(cmd *cobra.Command, args []string, state *commandState) bool {
	path := strings.TrimPrefix(cmd.CommandPath(), "kmx ")
	switch path {
	case "version", "completion", "help":
		return true
	}
	if state.contextFlag != "" {
		return false
	}
	switch path {
	case "run":
		// No explicit route, host input, live-Agent selector, or other operational
		// flag may turn the default package-free fake preview into external work.
		// Positional input has the same authority as the allowed inline prompt.
		if len(args) == 2 {
			args = args[:1]
		}
		return windowsFlagsAllowed(cmd, "case", "output", "prompt", "no-content", "verbose") && windowsPackageFreeFakeFile(args)
	case "test":
		return windowsFlagsAllowed(cmd, "case", "output", "no-content", "verbose") && windowsPackageFreeFakeFile(args)
	case "test explain", "eval explain", "package test explain":
		file := cmd.Flag("file")
		return len(args) <= 1 && file != nil && file.Changed && strings.TrimSpace(file.Value.String()) != "" &&
			windowsFlagsAllowed(cmd, "clear", "file", "show-content")
	}
	return false
}

func windowsPackageFreeFakeFile(args []string) bool {
	if len(args) != 1 {
		return false
	}
	agent, err := agentfile.Load(args[0])
	return err == nil && windowsPackageFreeFakeAgent(agent)
}

func windowsPackageFreeFakeAgent(agent *agentfile.Agent) bool {
	return agent != nil && agent.Model == "fake" && len(agent.Tools) == 0
}

func windowsFlagsAllowed(cmd *cobra.Command, names ...string) bool {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	valid := true
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !allowed[flag.Name] {
			valid = false
		}
	})
	return valid
}
