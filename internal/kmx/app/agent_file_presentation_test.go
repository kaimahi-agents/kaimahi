package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFilePreviewDistinguishesUnrunAndInconclusive(t *testing.T) {
	r := FilePreviewResult{Examples: []FilePreviewExample{
		{ID: "unrun", FilePreviewCompletion: FilePreviewCompletion{State: "not_run"}, Verdict: runtime.EvaluationUnknown},
		{ID: "inconclusive", FilePreviewCompletion: FilePreviewCompletion{State: "complete"}, Verdict: runtime.EvaluationUnknown},
	}}
	var out bytes.Buffer
	if err := WriteFilePreview(&out, r, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT_RUN unrun (0ms)") || !strings.Contains(out.String(), "UNKNOWN inconclusive (0ms)") {
		t.Fatalf("ambiguous states: %s", out.String())
	}
}
