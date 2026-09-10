// Command convert is an offline compatibility spike, not a kagent controller.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kaimahi-agents/kaimahi/spikes/kagent-shim/internal/convert"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: convert input.yaml schemas.json")
		return 1
	}
	input, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "convert: read input: %v\n", err)
		return 1
	}
	schemas, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "convert: read schemas: %v\n", err)
		return 1
	}
	output, err := convert.Convert(input, schemas)
	if err != nil {
		fmt.Fprintf(stderr, "convert: refused: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(output); err != nil {
		fmt.Fprintf(stderr, "convert: write output: %v\n", err)
		return 1
	}
	return 0
}
