//go:build !windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDirectPreviewCopilotSymlinkTempDir(t *testing.T) {
	target := diffTempDir(t)
	alias := filepath.Join(diffTempDir(t), "tmp")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	// Exercise valid fixtures under a macOS-style temp ancestor without changing
	// the parent's environment or the loader's deliberate symlink refusals.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.count=1", "-test.v", "-test.run=^TestDirectPreview(CopilotRunTestAndJudge|CopilotJudgeTransportFailuresStopSends|CopilotJudgeDiagnosticFailureStopsSends|LocalRoutesNeverAccessCopilot)$")
	cmd.Env = append(os.Environ(), "TMPDIR="+alias)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("preview fixtures under symlinked temp directory: %v\n%s", err, output)
	}
}
