package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var errWindowsOperationUnsupported = errors.New("this operation is not available in the guarded native Windows day-zero path")

const platformAnnotation = "kaimahi.dev/platform-gate"

// installPlatformGate refuses deferred Windows operations before configuration,
// state, subprocess, network, or ordinary output effects. Required help,
// completion and compatibility notices retain their existing output contracts.
// The compile guard remains until phase 5; GOOS injection exists only for policy
// tests while that guard is present.
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
				if windowsCommandAllowed(cmd, values) {
					if args != nil {
						return args(cmd, values)
					}
					return nil
				}
				if strings.HasPrefix(strings.TrimPrefix(cmd.CommandPath(), "kmx "), "eval") {
					return errors.Join(state.noticeLegacyEval(cmd), errWindowsOperationUnsupported)
				}
				if strings.TrimPrefix(cmd.CommandPath(), "kmx ") == "lift" {
					_, noticeErr := fmt.Fprintln(cmd.ErrOrStderr(), "kmx lift is deprecated; use kmx deploy (same arguments).")
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

func windowsCommandAllowed(cmd *cobra.Command, args []string) bool {
	path := strings.TrimPrefix(cmd.CommandPath(), "kmx ")
	switch path {
	case "version", "completion", "help":
		return true
	}
	// The command handlers retain their exact file-mode validation. A positional
	// FILE is required here so live Run and workspace Test never reach Args.
	if path == "run" || path == "test" {
		if len(args) != 1 {
			return false
		}
		for _, name := range []string{"provider", "endpoint", "tool-bindings", "prompt-file"} {
			if flag := cmd.Flag(name); flag != nil && flag.Changed {
				return false
			}
		}
		return true
	}
	if path == "test explain" || path == "eval explain" {
		return true
	}
	return false
}
