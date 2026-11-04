package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestMarkdownSuiteMappingPreservesNativeGraph(t *testing.T) {
	a := mdFixture(t)
	dest := filepath.Join(t.TempDir(), "mapped")
	result, err := MapMarkdownSuite(t.Context(), a.Path, "", dest, "qwen2.5:3b", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentDigest != a.Revision || result.TestsDigest != a.TestsRevision || !result.RequiresNativeEvaluation {
		t.Fatalf("%+v", result)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	prepared, err := workspace.Prepare(t.Context(), dest, cache)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if suite.Model != "qwen2.5:3b" || suite.Runtime.ID != "orka" || !bytes.Equal(suite.Instructions, []byte(a.Instructions)) || len(suite.Cases) != len(a.Examples) {
		t.Fatalf("mapped suite=%+v", suite)
	}
	if !bytes.Contains(suite.Cases[0].Bytes, []byte(`"id":"sam"`)) || !bytes.Contains(suite.Cases[0].Bytes, []byte(`"id":"greeting"`)) {
		t.Fatalf("case=%s", suite.Cases[0].Bytes)
	}
	again := filepath.Join(t.TempDir(), "mapped")
	if _, err = MapMarkdownSuite(t.Context(), a.Path, "", again, "qwen2.5:3b", ""); err != nil {
		t.Fatal(err)
	}
	next, err := workspace.Prepare(t.Context(), again, cache)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Descriptor.Digest != next.Descriptor.Digest {
		t.Fatal("mapping is not deterministic")
	}
	if _, err = MapMarkdownSuite(t.Context(), a.Path, "", dest, "qwen2.5:3b", ""); err == nil {
		t.Fatal("existing workspace replaced")
	}
	source, _ := os.ReadFile(a.Path)
	if !bytes.Equal(source, a.Source) {
		t.Fatal("source modified")
	}
}

func TestMarkdownSuiteMappingRefusesBeforeDestinationWrites(t *testing.T) {
	a := mdFixture(t)
	for _, tc := range []struct{ source, model, name string }{{a.Path, "auto", ""}, {a.Path, "", ""}, {a.Path, "qwen2.5:3b", "INVALID_NAME"}, {"../../../.github/agents/weather.agent.md", "qwen2.5:3b", ""}} {
		dest := filepath.Join(t.TempDir(), "absent")
		if _, err := MapMarkdownSuite(t.Context(), tc.source, "", dest, tc.model, tc.name); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination written for invalid mapping: %v", err)
		}
	}
}
