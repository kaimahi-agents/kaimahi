package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func titlePair(t *testing.T, title string) *agentfile.Pair {
	t.Helper()
	p := previewPair(t)
	source, err := os.ReadFile(p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(title)
	source = bytes.Replace(source, []byte("id: external\n"), []byte("id: external\ntitle: "+string(value)+"\n"), 1)
	if err := os.WriteFile(p.Tests.Selection.Path, source, 0600); err != nil {
		t.Fatal(err)
	}
	p, err = agentfile.LoadPair(p.AgentPath, p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMachineTestTitleWithholding(t *testing.T) {
	for _, mode := range []string{"safe", "prose", "before-ready", "credential", "late-redaction", "id-withheld"} {
		t.Run(mode, func(t *testing.T) {
			title := "Says hello é 🙂"
			id := "first"
			if mode == "prose" {
				title = "Host: display label"
			}
			if mode == "credential" {
				title = "opaquecredential"
			}
			if mode == "id-withheld" {
				id = "opaquecredential"
			}
			var events []MachineFileProgress
			m := NewMachineFileObserver("embedded", func(p MachineFileProgress) error { events = append(events, p); return nil })
			m.bind([]agentfile.Example{{ID: id, Title: title}}, "revision", "snapshot", modelaccess.Route{Provider: "fake", Model: "fake"})
			if mode != "before-ready" {
				if err := m.ready(func(values ...string) error {
					for _, v := range values {
						if strings.Contains(v, "opaquecredential") {
							return errors.New("withheld")
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := m.observed(agentpreview.Observation{Event: "case.started", CaseIndex: 1, FileRevision: "revision", SnapshotRevision: "snapshot", Provider: "fake", Model: "fake"}); err != nil {
					t.Fatal(err)
				}
				m.finished(FilePreviewCaseProgress{CaseID: id})
			}
			result := FilePreviewResult{}
			if mode == "late-redaction" {
				result.ReasonCode = "credential_content"
			}
			final := m.Final(result)
			encoded, _ := json.Marshal(final.Cases[0])
			var row map[string]any
			if err := json.Unmarshal(encoded, &row); err != nil {
				t.Fatal(err)
			}
			want := mode == "safe" || mode == "prose"
			if (row["title"] != nil) != want || want && row["title"] != title {
				t.Fatalf("wrong final title: %s", encoded)
			}
			for _, event := range events {
				encoded, _ := json.Marshal(event)
				var p map[string]any
				_ = json.Unmarshal(encoded, &p)
				if p["caseId"] == nil && p["title"] != nil {
					t.Fatalf("orphan title: %s", encoded)
				}
				if p["caseId"] != nil && want && p["title"] != title {
					t.Fatalf("progress lost title: %s", encoded)
				}
				if strings.Contains(string(encoded), "opaquecredential") {
					t.Fatalf("credential leaked: %s", encoded)
				}
			}
		})
	}
}

func TestFileTestTitleCredentialPreflight(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	p := titlePair(t, "opaquecredential")
	provider := &journalCopilotAccess{}
	runner := localpreview.NewRunner(localpreview.RunnerOptions{Access: provider.PreviewClient, JournalDir: func() (string, error) { return t.TempDir(), nil }})
	route := copilotPreviewRoute()
	result, err := (&App{FileRunner: runner}).PreviewAgentPairSnapshot(t.Context(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test", Model: route.Model, Route: &route})
	encoded, _ := json.Marshal(result)
	if err == nil || result.ReasonCode != "credential_content" || provider.sends != 0 || strings.Contains(string(encoded), "opaquecredential") {
		t.Fatalf("title credential escaped preflight: %v %s sends=%d", err, encoded, provider.sends)
	}
}

func TestFileTestTitleWithheldBeforePreflight(t *testing.T) {
	for _, early := range []string{"cancelled", "settings", "setup-failure"} {
		t.Run(early, func(t *testing.T) {
			p := titlePair(t, "opaquecredential")
			ctx := t.Context()
			opt := FilePreviewOptions{File: p.AgentPath, Operation: "test", Model: "fake"}
			runner := localpreview.NewRunner(localpreview.RunnerOptions{
				Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
					return nil, agentpreview.ErrModelUnavailable
				},
				JournalDir: func() (string, error) { t.Fatal("early refusal opened journal"); return "", nil },
			})
			switch early {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "settings":
				opt.ExpectedToolSettings = &[]localtools.ToolSetting{{Name: "changed", State: localtools.Enabled}}
			case "setup-failure":
				route := copilotPreviewRoute()
				opt.Route, opt.Model = &route, route.Model
			}
			result, err := (&App{FileRunner: runner}).PreviewAgentPairSnapshot(ctx, p, opt)
			if err == nil {
				t.Fatal("early refusal accepted")
			}
			encoded, _ := json.Marshal(result)
			var text bytes.Buffer
			_ = WriteFilePreview(&text, result, "text")
			var summary bytes.Buffer
			_ = WriteFilePreviewGitHubSummary(&summary, result)
			if strings.Contains(string(encoded)+text.String()+summary.String(), "opaquecredential") {
				t.Fatal("unchecked title reached early-refusal output")
			}
		})
	}
}

func TestFileTestTitleSummaryByteBound(t *testing.T) {
	result := summaryFixture()
	result.Examples = nil
	result.Selection.CaseIDs = nil
	result.Selection.TotalCases = 128
	result.ToolSettings = nil
	for j := range 4 {
		result.ToolSettings = append(result.ToolSettings, localtools.ToolSetting{Name: fmt.Sprintf("tool-%d", j), State: localtools.Enabled})
	}
	for i := range 128 {
		ex := summaryFixture().Examples[0]
		ex.ID, ex.Title = fmt.Sprintf("case-%d", i), strings.Repeat("~", 160)
		ex.Assertions, ex.Tools, ex.ToolCalls = nil, nil, 4
		for j := range 4 {
			as := summaryFixture().Examples[0].Assertions[0]
			as.ID = fmt.Sprintf("check-%d", j)
			ex.Assertions = append(ex.Assertions, as)
			ex.Tools = append(ex.Tools, agentpreview.ToolSummary{Name: fmt.Sprintf("tool-%d", j), Mode: "recorded", Source: "recorded", CallCount: 1})
		}
		result.Examples = append(result.Examples, ex)
		result.Selection.CaseIDs = append(result.Selection.CaseIDs, ex.ID)
	}
	var out bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&out, result); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 1<<20 {
		t.Fatalf("supported titles exceed the GitHub step limit: %d bytes", out.Len())
	}
}

func TestFileTestTitleSummaryLiteral(t *testing.T) {
	title := "<b>@someone</b> | `code` [link](https://example.test)"
	result := summaryFixture()
	result.Examples[0].Title = title
	var out bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&out, result); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "someone") != 1 || !strings.Contains(out.String(), "greeting") {
		t.Fatalf("missing label: %s", &out)
	}
	for _, active := range []string{"<b>", "@someone", " | `code`", "[link](https://example.test)"} {
		if strings.Contains(out.String(), active) {
			t.Fatalf("active markup %q: %s", active, &out)
		}
	}
	for _, title := range []string{"Host: display label", "\tbad", "bad\u202e", strings.Repeat("x", 161)} {
		result.Examples[0].Title = title
		out.Reset()
		err := WriteFilePreviewGitHubSummary(&out, result)
		if (title == "Host: display label") != (err == nil) {
			t.Fatalf("wrong plain-title summary validation for %q: %v", title, err)
		}
	}
}
