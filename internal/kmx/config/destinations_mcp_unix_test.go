//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDestinationsIgnoreMCPOnlySemantics(t *testing.T) {
	for _, suffix := range []string{
		"mcpServers:\n  local:\n    command: 4\n    args: [true, null, \"\", \" x\"]\n    tools: {ping: [invalid]}\nliveToolServers: [unknown]\n",
		"mcpServers: false\nliveToolServers: null\n",
	} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte("version: 1\nevalTarget: dev\ntargets:\n  dev: {context: test}\n"+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		doc, err := LoadDestinations(dir)
		if err != nil || doc.EvalTarget != "dev" {
			t.Fatalf("MCP-only semantics blocked destinations: %+v %v", doc, err)
		}
	}
}

func TestDestinationsMCPStructureStillChecked(t *testing.T) {
	for _, suffix := range []string{
		"mcpServers: {x: {command: one, command: two}}\n",
		"mcpServers: {x: &s {command: one}}\nliveToolServers: [*s]\n",
		"mcpServers: {x: {<<: {command: one}}}\n",
		"mcpServers: {}\nmcpServers: {}\n",
		"liveToolServer: []\n",
		"targets: {dev: {context: test, unknown: ignored}}\n",
	} {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte("version: 1\n"+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDestinations(dir); err == nil {
			t.Fatalf("ambiguous/unknown operational config accepted: %s", suffix)
		}
	}
}
