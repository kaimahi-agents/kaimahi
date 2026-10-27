// Package main implements kmx command parsing, help and presentation.
// Cobra handles CLI syntax; command adapters connect user requests to
// application operations. File Run/Test default to the canvas's Copilot route,
// show Run answers and failed-check content unless --no-content is selected,
// and map completed failures to exit 1 and execution refusals to exit 2.
// The guarded Windows tree additionally classifies
// deferred operations before their argument or operational hooks can run.
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
	var file *fileCommandError
	if errors.As(err, &file) {
		return file.exit
	}
	var preview *app.FilePreviewError
	if errors.As(err, &preview) {
		return preview.ExitCode()
	}
	if errors.Is(err, app.ErrTaskPending) || errors.Is(err, app.ErrDestinationStatusPending) {
		return 2
	}
	return 1
}
