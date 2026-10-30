package app

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func cliSuggestionFixture(t *testing.T) (*MarkdownAgent, MarkdownToolProposal) {
	t.Helper()
	a := mdFixture(t)
	s := CLIToolSpec{Name: "go_version", Description: "Read local Go version", Command: "go", Args: []CLIToolArg{{Literal: "version"}}, Parameters: map[string]CLIToolParameter{}}
	s.Execution.Timeout = "5s"
	s.Execution.MaxOutputBytes = 1024
	s.Execution.Approval = "granted-scope"
	second := s
	second.Name = "git_version"
	second.Command = "git"
	second.Description = "Read Git version"
	p := MarkdownToolProposal{SchemaVersion: 2, Source: a.Path, AgentDigest: a.Revision, CLIChoices: []MarkdownCLIChoice{{Reason: "Check Go compatibility", Scope: "Only go version; no repository changes", MissingScope: []string{}, Tool: &s}, {Reason: "Check Git compatibility", Scope: "Only git version; no repository changes", MissingScope: []string{}, Tool: &second}}, Gaps: []string{}}
	p.ToolsDigest = a.ToolsRevision
	return a, p
}

func TestImportToolSuggestionsAllowSubsetWithoutGrant(t *testing.T) {
	a, p := cliSuggestionFixture(t)
	if err := AddSuggestedCLITools(t.Context(), p, []string{"go_version"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.Path)
	got, err := parseMarkdownAgent(a.Path, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadMarkdownTools(got); err != nil {
		t.Fatal(err)
	}
	if len(got.CLITools) != 1 || got.CLITools[0].Name != "go_version" || got.Instructions != a.Instructions {
		t.Fatalf("got=%+v", got)
	}
	if g, _, err := readCurrentToolGrant(a.Path); err != nil || g != nil {
		t.Fatal("declaration acceptance granted execution")
	}
}

func TestImportToolSuggestionsDenyCancelAndMissingScope(t *testing.T) {
	a, p := cliSuggestionFixture(t)
	if err := AddSuggestedCLITools(t.Context(), p, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.Path)
	if !bytes.Equal(raw, a.Source) {
		t.Fatal("empty selection changed source")
	}
	p.CLIChoices = []MarkdownCLIChoice{{Reason: "Inspect a cluster", Scope: "Not known", MissingScope: []string{"subscription", "resource group"}, Tool: nil}}
	if _, err := CLIProposalSelection(p, nil); err == nil {
		t.Fatal("unscoped tool was selectable")
	}
}

func TestGenericSuggestionsAreBoundedAndOfflineUntilInference(t *testing.T) {
	a, p := cliSuggestionFixture(t)
	raw, _ := json.Marshal(map[string]any{"tools": p.CLIChoices, "gaps": p.Gaps})
	e := &suggestionExecutor{output: string(raw)}
	proposal, err := suggestCLICommandTools(t.Context(), a.Path, "", e)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || proposal.SchemaVersion != 2 || len(proposal.CLIChoices) != 2 {
		t.Fatal("wrong proposal")
	}
	read, err := ReadMarkdownToolProposal(proposal.ID)
	if err != nil || len(read.CLIChoices) != 2 {
		t.Fatal(err)
	}
	for _, mutate := range []func(*MarkdownToolProposal){
		func(p *MarkdownToolProposal) { p.CLIChoices = append(p.CLIChoices, p.CLIChoices...) },
		func(p *MarkdownToolProposal) { p.CLIChoices[0].Tool.Command = "bash" },
		func(p *MarkdownToolProposal) { p.CLIChoices[0].Tool.Execution.Timeout = "60s" },
	} {
		_, bad := cliSuggestionFixture(t)
		mutate(&bad)
		b, _ := json.Marshal(map[string]any{"tools": bad.CLIChoices, "gaps": bad.Gaps})
		if _, _, err := decodeCLICommandSuggestions(string(b)); err == nil {
			t.Fatal("invalid suggestion accepted")
		}
	}
	if err = os.WriteFile(a.Path, append(bytes.Clone(a.Source), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err = AddSuggestedCLITools(t.Context(), proposal, []string{"go_version"}); err == nil {
		t.Fatal("stale proposal edited source")
	}
}
