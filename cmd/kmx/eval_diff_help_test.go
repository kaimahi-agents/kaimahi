package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvalDiffHelpDescribesNativePassingEvidenceOnly(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	if err := execute([]string{"eval", "diff", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "full-passing native Orka/kagent suite evidence") || !strings.Contains(body, "AgentSessions and legacy receipts are refused") {
		t.Fatal("diff help does not describe the retained reader boundary")
	}
	if strings.Contains(body, "Legacy receipts are recognized") || *loads != 0 {
		t.Fatal("diff help retains removed capabilities or loads configuration")
	}
}
