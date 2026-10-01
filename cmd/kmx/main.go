// Command kmx is the CLI entry point for Kaimahi's developer and governance
// workflows. Cobra owns syntax and help; internal/kmx/app owns all operations.
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
	if errors.Is(err, app.ErrTaskPending) {
		return 2
	}
	return 1
}
