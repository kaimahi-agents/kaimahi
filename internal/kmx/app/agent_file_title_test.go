package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
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

func TestFileTestTitleSummaryLiteral(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	title := "<b>@someone</b> | `code` [link](https://example.test)"
	p := titlePair(t, title)
	runner := localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return t.TempDir(), nil }})
	result, err := (&App{FileRunner: runner}).PreviewAgentPairSnapshot(context.Background(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test", Model: "fake"})
	// This fixture can fail its assertion; the label still belongs in the summary.
	if result.State != "complete" {
		t.Fatalf("preview not complete: %v %+v", err, result)
	}
	var out bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&out, result); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "someone") != 2 || !strings.Contains(out.String(), "external") {
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
