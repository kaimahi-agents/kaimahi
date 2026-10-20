//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsBuildExplainsWSLRequirement(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(t.TempDir(), "kmx.exe"), ".")
			cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0")
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("Windows build timed out: %s", output)
			}
			if err == nil {
				t.Fatal("Windows build succeeded despite unsupported filesystem safety")
			}
			message := string(output)
			for _, want := range []string{"Windows-is-not-supported-yet", "use-WSL", "github.com/kaimahi-agents/kaimahi/issues/578"} {
				if !strings.Contains(message, want) {
					t.Errorf("Windows build missing %q: %s", want, message)
				}
			}
			if strings.Contains(message, "undefined: unix.") {
				t.Errorf("Windows build reached Unix-only dependencies: %s", message)
			}
		})
	}
}
