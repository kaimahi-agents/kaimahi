// Package main implements kmx command parsing, help and presentation.
// Cobra handles CLI syntax; command adapters connect user requests to
// application operations.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "__agent-manager" {
		if err := app.RunAgentManager(); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "__agent-worker" {
		if err := app.RunAgentWorker(); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := execute(os.Args[1:], productionDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	var preview *app.FilePreviewError
	if errors.As(err, &preview) {
		return preview.ExitCode()
	}
	if errors.Is(err, app.ErrTaskPending) || errors.Is(err, app.ErrDestinationStatusPending) {
		return 2
	}
	return 1
}
