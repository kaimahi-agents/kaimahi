package workspace_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Byte-equivalent legacy caches must not stand in for the current packing policy.
func TestPrepareRefusesLegacyPackingWithoutReplacingCache(t *testing.T) {
	for _, policy := range []string{"", "future-policy"} {
		t.Run(policy, func(t *testing.T) {
			root, cache := createWorkspace(t)
			before, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			entry := filepath.Dir(before.Path)
			record := filepath.Join(entry, "prepared.json")
			original := readFile(t, record)
			var doc map[string]any
			if err := json.Unmarshal(original, &doc); err != nil {
				t.Fatal(err)
			}
			delete(doc, "packaging")
			if policy != "" {
				doc["packaging"] = policy
			}
			legacy, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, legacy, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil || !strings.Contains(err.Error(), "packaging policy") || !strings.Contains(err.Error(), entry) || !strings.Contains(err.Error(), "move aside") {
				t.Fatalf("legacy cache needs an actionable refusal, got %v", err)
			}
			if !bytes.Equal(legacy, readFile(t, record)) {
				t.Fatal("legacy cache was repaired or replaced")
			}
			if err := os.Rename(entry, entry+".saved"); err != nil {
				t.Fatal(err)
			}
			after, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			if before.Descriptor.Digest != after.Descriptor.Digest || before.Fingerprint != after.Fingerprint {
				t.Fatal("cache migration unnecessarily changed native artifact identity")
			}
			again, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil || again.Path != after.Path || again.Descriptor.Digest != after.Descriptor.Digest {
				t.Fatal("current-policy cache was not reusable", err)
			}
		})
	}
}
