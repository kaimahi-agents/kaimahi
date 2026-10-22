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
