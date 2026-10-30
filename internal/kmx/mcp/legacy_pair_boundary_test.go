package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyMCPGithubAgentsRootProbesSiblingPresence(t *testing.T) {
	base := physicalDir(t)
	root := filepath.Join(base, ".github", "agents")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "g.agent.md")
	source := []byte("# Greeter\nSay hello.\n")
	writeSource(t, file, source)
	files, err := OpenFiles(root, parseValidator)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if _, err := files.snapshot(t.Context(), "g.agent.md"); err != nil {
		t.Fatalf("unpaired legacy read refused: %v", err)
	}
	if _, err := files.Edit(t.Context(), "g.agent.md", revision(source), source); err != nil {
		t.Fatalf("unpaired legacy edit refused: %v", err)
	}
	// The sibling probe grants no source-content or authoring authority there.
	tests := filepath.Join(base, ".github", "agent-tests", "g.agent.test.md")
	writeSource(t, tests, []byte("not a tests document"))
	_, err = files.snapshot(t.Context(), "g.agent.md")
	requireCode(t, err, "paired_source_required")
	_, err = files.Edit(t.Context(), "g.agent.md", revision(source), source)
	requireCode(t, err, "paired_source_required")
	_, err = files.Create(t.Context(), "../agent-tests/other.agent.md", source)
	requireCode(t, err, "unsafe_path")
	requireBytes(t, file, source)
	requireBytes(t, tests, []byte("not a tests document"))
}

func TestLegacyMCPPairRefusalSurvivesToolBoundary(t *testing.T) {
	files, root, source := canonicalFiles(t)
	file, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	writeSource(t, filepath.Join(root, "greeter.agent.test.md"), []byte("invalid tests source"))
	runner := &previewFixture{}
	client := connectFixture(t, Operations(files, &app.App{FileRunner: runner}, nil))
	for _, name := range []string{"run", "test", "create", "edit"} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"schemaVersion": 1, "path": file.Path}
			if name == "create" || name == "edit" {
				args["content"] = source
			}
			if name == "edit" {
				args["expectedRevision"] = file.Revision
			}
			response, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
			var got struct{ ReasonCode, Reason string }
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(response.StructuredContent)
			if json.Unmarshal(b, &got) != nil || !response.IsError || got.ReasonCode != "paired_source_required" || !strings.Contains(got.Reason, "kmx test FILE") {
				t.Fatalf("lost companion refusal: %s %v", b, err)
			}
			if runner.calls != 0 {
				t.Fatal("refused source reached runner")
			}
			requireBytes(t, filepath.Join(root, file.Path), []byte(source))
		})
	}
}

func TestLegacyMCPUnsafePathPrecedesPairProbe(t *testing.T) {
	files, _, source := canonicalFiles(t)
	client := connectFixture(t, Operations(files, &app.App{}, nil))
	response, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 1, "path": "../escape.agent.md", "content": source}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(response.StructuredContent)
	if !response.IsError || !strings.Contains(string(b), `"reasonCode":"unsafe_path"`) {
		t.Fatalf("path refusal replaced by pair probe: %s", b)
	}
}

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
