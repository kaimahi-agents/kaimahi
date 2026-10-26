//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDestinationsSharesFolderWithToolSettings(t *testing.T) {
	for _, tc := range []struct {
		tools string
		valid bool
	}{{"{ping: Disabled, lookup: Enabled, version: Disabled, plainHTTP: Enabled}", true}, {"{ping: Ask}", false}, {"{ping: false}", false}, {"{bad.name: Disabled}", false}} {
		t.Run(tc.tools, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			raw := "version: 1\npublishRepository: registry.example/team/agents\ntargets: {dev: {context: kind-test}}\ntools: " + tc.tools + "\n"
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			doc, err := LoadDestinations(root)
			if (err == nil) != tc.valid || tc.valid && (doc == nil || doc.PublishRepository != "registry.example/team/agents") {
				t.Fatalf("coexisting config: %+v %v", doc, err)
			}
		})
	}
}
