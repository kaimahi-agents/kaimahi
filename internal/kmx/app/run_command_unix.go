//go:build !windows

package app

import (
	"os/exec"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func preparedExecCommand(cmd *run.Command) *exec.Cmd { return cmd }
