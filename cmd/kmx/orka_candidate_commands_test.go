package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOrkaCandidateFlagsRefuseUnsupportedRoutesBeforeConfiguration(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"test", "agent.md", "--orka-chart", "candidate.tgz", "--orka-chart-sha256", digest},
		{"test", "agent.md", "--orka-chart=", "--orka-chart-sha256="},
		{"test", "--development-host", "host.json", "--orka-chart", "candidate.tgz", "--orka-chart-sha256", digest},
		{"deploy", "--orka-chart", "candidate.tgz", "--orka-chart-sha256", digest},
		{"deploy", "--orka-chart=", "--orka-chart-sha256="},
		{"lift", "--orka-chart", "candidate.tgz", "--orka-chart-sha256", digest},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			if err := execute(args, deps); err == nil || *loads != 0 || out.Len() != 0 {
				t.Fatalf("unsupported candidate route proceeded: err=%v loads=%d stdout=%q", err, *loads, &out)
			}
		})
	}
}

func TestFileTestRefusesLocalChartBeforePreview(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, flags := range [][]string{
		{"--orka-chart", "candidate.tgz"},
		{"--orka-chart-sha256", digest},
		{"--orka-chart", "candidate.tgz", "--orka-chart-sha256", digest},
		{"--orka-chart=", "--orka-chart-sha256="},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &cliPreviewFake{}
			deps.fileRunner = runner
			err := execute(append([]string{"test", cliPreviewFile(t)}, flags...), deps)
			if err == nil || exitCode(err) != 2 || runner.calls != 0 || *loads != 0 || out.Len() != 0 {
				t.Fatalf("err=%v calls=%d loads=%d stdout=%q", err, runner.calls, *loads, &out)
			}
		})
	}
}
