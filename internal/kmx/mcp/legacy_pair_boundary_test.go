package mcp

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLegacyMCPDoesNotIgnoreDefaultTestsFile(t *testing.T) {
	root := physicalDir(t)
	file := filepath.Join(root, "g.agent.md")
	source := []byte("# Greeter\nSay hello.\n")
	writeSource(t, file, source)
	files, err := OpenFiles("", parseValidator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.snapshot(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	// Even an invalid selected tests file is not grounds to execute or edit only
	// the agent. Paired CLI access is separate from these legacy MCP capabilities.
	writeSource(t, filepath.Join(root, "g.agent.test.md"), []byte("invalid tests source"))
	for _, operation := range []string{"read", "edit"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			if operation == "read" {
				_, err = files.snapshot(t.Context(), file)
			} else {
				_, err = files.Edit(t.Context(), file, revision(source), source)
			}
			var refused *Error
			if !errors.As(err, &refused) || refused.Code != "paired_source_required" {
				t.Fatalf("legacy %s ignored selected tests: %v", operation, err)
			}
			requireBytes(t, file, source)
		})
	}
}
