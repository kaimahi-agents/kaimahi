package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAzureDiscoveryFlagsRemovedBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"chat", "demo", "--azure-discovery", "sdk"},
		{"cluster", "quickstart", "--azure-discovery", "sdk"},
		{"cluster", "quickstart", "--interactive", "--azure-discovery", "cli"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "unknown flag: --azure-discovery") {
				t.Fatalf("retired discovery selector still accepted: %v", err)
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatal("removed discovery selector performed work")
			}
		})
	}
}
