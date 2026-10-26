package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPSquadStartupConfigurationAndInertDiscovery(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "squad"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	program := filepath.Join(t.TempDir(), name)
	if err := os.Link(exe, program); err != nil {
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, b, 0755); err != nil {
			t.Fatal(err)
		}
	}
	sr, cw, _ := os.Pipe()
	cr, sw, _ := os.Pipe()
	defer sr.Close()
	defer cw.Close()
	defer cr.Close()
	defer sw.Close()
	var ignored, diag bytes.Buffer
	deps, loads := testDependencies(&ignored, &diag)
	deps.stdin, deps.stdout = sr, sw
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := newRootCommand(&commandState{deps: deps})
	cmd.SetArgs([]string{"mcp", "--root", root, "--squad-program", program, "--squad-release", "1.1.0"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "native-startup", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatal("missing four native-enabled tools and canonical settings writer")
	}
	for _, tool := range tools.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var schema map[string]any
		if json.Unmarshal(b, &schema) != nil {
			t.Fatal("invalid schema")
		}
		if schema["oneOf"] != nil || schema["anyOf"] != nil || schema["allOf"] != nil {
			t.Fatal("top-level schema union can break model tool loading")
		}
		versions := 2
		if tool.Name == "set_tool_state" {
			versions = 1
		}
		if len(schema["properties"].(map[string]any)["schemaVersion"].(map[string]any)["enum"].([]any)) != versions {
			t.Fatal("native operation not advertised")
		}

	}
	if _, err := os.Stat(filepath.Join(root, ".squad")); !os.IsNotExist(err) || *loads != 0 {
		t.Fatal("discovery initialized project or loaded runtime config")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestMCPSquadFlagsRequireExplicitQualifiedPair(t *testing.T) {
	for _, extra := range [][]string{{"--squad-program", "relative/squad"}, {"--squad-release", "1.1.0"}, {"--squad-program", "", "--squad-release", "1.1.0"}, {"--squad-program", "/missing/squad", "--squad-release", "2.0.0"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(append([]string{"mcp", "--root", t.TempDir()}, extra...), deps)
		if err == nil || strings.Contains(err.Error(), "unknown flag") || out.Len() != 0 || *loads != 0 {
			t.Fatalf("missing safe native config validation: %v", err)
		}
	}
}
