//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCheckToolsRunsActivationProbesWithoutConfig(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	dir := t.TempDir()
	file := filepath.Join(dir, "check.agent.md")
	if err := os.WriteFile(file, []byte("---\nname: check\nmodel: fake\ntools: [check]\n---\nCall check.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "check-command")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'tool version 1.2.3'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("tools: {check: Enabled}\ncommandTools:\n  check:\n    command: %q\n    args: [run]\n    checks:\n      compatibility:\n        args: [version]\n        timeout: 5s\n        expect: {exitCode: 0, stdoutRegex: '^tool version 1\\.2\\.3$'}\n", wrapper)
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"run", file, "--check-tools", "-o", "json"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), `"kind":"kmx.local-tool-checks"`) || !strings.Contains(out.String(), `"check":"compatibility"`) || !strings.Contains(out.String(), `"state":"passed"`) {
		t.Fatalf("loads=%d out=%q diagnostics=%q", *loads, out.String(), diagnostics.String())
	}
}
