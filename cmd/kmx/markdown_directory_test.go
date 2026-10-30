package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkdownDirectoryImportAndInspectionOffline(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	source := filepath.Join(t.TempDir(), "weather.agent.md")
	raw := []byte("---\nname: weather\ndescription: weather\ntools: []\n---\nSuggest clothing.\n")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "weather")
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"import", source, "--into", dest, "-o", "json"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 {
		t.Fatal("import loaded cluster config")
	}
	for _, cmd := range []string{"validate", "inspect"} {
		out.Reset()
		if err := execute([]string{cmd, dest, "-o", "yaml"}, deps); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dest, "weather.agent.md"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("import rewrote source", err)
	}
}
