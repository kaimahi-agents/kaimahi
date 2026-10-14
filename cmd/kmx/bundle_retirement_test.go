package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBundleCommandsRemovedBeforeConfiguration(t *testing.T) {
	for _, operation := range []string{"", "lift", "evaluate", "retire", "verify", "status"} {
		t.Run(operation, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			args := []string{"bundle"}
			if operation != "" {
				args = append(args, operation)
			}
			err := execute(args, deps)
			if err == nil || !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("bundle still registered: %v", err)
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatalf("removed route loaded configuration or emitted output: loads=%d output=%q", *loads, out.String())
			}
		})
	}
}

func TestCreateLiveFlagsRemovedBeforeConfiguration(t *testing.T) {
	for _, flag := range []string{"live", "out", "no-apply", "dry-run", "bundle-path", "provider-type", "secret", "base-url", "tools", "skills", "task"} {
		t.Run(flag, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute([]string{"create", "sample", "--" + flag + "=true"}, deps)
			if err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("legacy flag %s still registered: %v", flag, err)
			}
			if *loads != 0 || out.Len() != 0 {
				t.Fatal("removed flag performed work")
			}
		})
	}
}
