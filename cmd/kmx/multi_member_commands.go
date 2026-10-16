package main

import (
	"errors"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Recognize only the new offline-only boundary. Every other resolution error is
// left to the command's existing validator, preserving precedence and redaction.
// This preflight never supplies a workspace or authorizes an operation.
func refuseMultiMemberWorkspace(override string) error {
	start, err := os.Getwd()
	if err != nil {
		return nil
	}
	root, err := workspace.Resolve(start, override)
	if err == nil && errors.Is(workspace.RequireSingleMember(root), workspace.ErrMultiMemberUnsupported) {
		return workspace.ErrMultiMemberUnsupported
	}
	return nil
}
