package app

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFileToolAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, mode, source string
		state              localtools.ToolState
		calls              int
		want               bool
	}{
		{"pending", "recorded", "unavailable", localtools.Enabled, 0, true},
		{"requested", "recorded", "unavailable", localtools.Enabled, 1, true},
		{"implicit enabled", "live", "unavailable", "", 0, true},
		{"disabled", "recorded", "unavailable", localtools.Disabled, 0, false},
		{"recorded", "recorded", "recorded", localtools.Enabled, 1, false},
		{"capturing", "recorded", "live", localtools.Enabled, 1, false},
		{"live", "live", "live", localtools.Enabled, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := summaryFixture()
			r.Examples[0].Tools[0] = agentpreview.ToolSummary{Name: "lookup", Mode: tc.mode, Source: tc.source, CallCount: tc.calls}
			r.Examples[0].ToolCalls = tc.calls
			r.ToolSettings = nil
			if tc.state != "" {
				r.ToolSettings = []localtools.ToolSetting{{Name: "lookup", State: tc.state}}
			}
			for _, adHoc := range []bool{false, true} {
				textResult := r
				if adHoc {
					textResult.AdHoc = &r.Examples[0].FilePreviewCompletion
					textResult.Examples = nil
				}
				var text bytes.Buffer
				if err := WriteFilePreview(&text, textResult, "text"); err != nil {
					t.Fatal(err)
				}
				if got := strings.Contains(text.String(), "No recording; tool unavailable."); got != tc.want {
					t.Fatalf("adHoc=%v availability note=%v, want %v:\n%s", adHoc, got, tc.want, text.String())
				}
			}
			var summary bytes.Buffer
			if err := WriteFilePreviewGitHubSummary(&summary, r); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(summary.String(), "Unavailable tools have no recording or live source"); got != tc.want {
				t.Fatalf("summary availability note=%v, want %v:\n%s", got, tc.want, summary.String())
			}
		})
	}
}

func TestFilePreviewUnavailableSummaryBoundsLegend(t *testing.T) {
	for _, firstVisible := range []bool{false, true} {
		r := summaryFixture()
		r.ToolSettings = nil
		r.Examples = nil
		r.Selection.CaseIDs = nil
		// Seventeen tests of 32 tools puts the final tool beyond the 512-row cap.
		for i := range 17 {
			ex := summaryFixture().Examples[0]
			ex.ID = string(rune('a' + i))
			ex.Tools = nil
			ex.ToolCalls = 0
			for j := range 32 {
				source := "recorded"
				if i == 16 && j == 31 || firstVisible && i == 0 {
					source = "unavailable"
				}
				ex.Tools = append(ex.Tools, agentpreview.ToolSummary{Name: "tool_" + strconv.Itoa(j), Mode: "recorded", Source: source})
			}
			r.Examples = append(r.Examples, ex)
			r.Selection.CaseIDs = append(r.Selection.CaseIDs, ex.ID)
		}
		r.Selection.TotalCases = len(r.Examples)
		var summary bytes.Buffer
		if err := WriteFilePreviewGitHubSummary(&summary, r); err != nil {
			t.Fatal(err)
		}
		want := 0
		if firstVisible {
			want = 1
		}
		if got := strings.Count(summary.String(), "Unavailable tools have no recording or live source"); got != want {
			t.Fatalf("legend count %d, want %d", got, want)
		}
	}
}
