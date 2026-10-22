// Command kmx is the CLI entry point for Kaimahi's developer workflows.
// Cobra owns syntax and help; internal/kmx/app owns all operations.
//
// The command adapters validate arguments and own help, completion and
// presentation. They cover native workspace create, test, push and Deploy, Orka
// operations, live run and chat with Task results, quickstart and console,
// context and local commands, hidden AKS setup and teardown, read-only targets,
// suite transfer and offline policy resolution.
//
// Stdio `mcp --root DIRECTORY` binds Create, Edit, Run and Test to one explicit
// root and the shared snapshot result, without a separate runner or
// tool-selected endpoints.
//
// Markdown-file Run and Test use detached snapshots and bounded safe preview
// projection. A temporary direct no-journal runner uses the selected
// model-access transport, a deterministic fake and one-sample preview judging
// until embedded runner integration. This is not Squad registration or durable
// execution proof.
//
// Create prompts for missing exact model and file inputs only at a terminal;
// scripts must supply both. Composed Orka workspace Deploy has stage, verbose
// and JSON presentation alongside explicit digest Deploy. Named destinations
// resolve once; ctx is an offline list with optional bounded local-context and
// anonymous registry checks, not execution proof. Offline test explain reads
// bounded owner-only failure records with explicit content opt-in. Portable
// test export requires fresh captured build provenance; test verify is a
// state-free gate over independently reviewed expectations. Receipt diff
// accepts strict full-passing Orka and kagent proof, not AgentSessions proof.
// Removed bundle and live-create routes stay unknown; interactive Deploy only
// prints the move notice.
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
