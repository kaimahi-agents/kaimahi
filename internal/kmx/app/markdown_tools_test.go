package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type suggestionExecutor struct {
	calls  int
	output string
}

func (e *suggestionExecutor) Start(context.Context) (string, error) { return "fake", nil }
func (e *suggestionExecutor) Reset()                                {}
func (e *suggestionExecutor) Close()                                {}
func (e *suggestionExecutor) Answer(_ context.Context, a *MarkdownAgent, prompt string, mocks []MarkdownMock) (markdownAnswer, error) {
	e.calls++
	if len(a.Tools) != 0 || a.Grant != nil || mocks != nil {
		panic("suggestion obtained tools")
	}
	return markdownAnswer{Text: e.output}, nil
}

func TestToolSuggestionsDoNotGrantOrEdit(t *testing.T) {
	a := mdFixture(t)
	e := &suggestionExecutor{output: `{"choices":[{"template":"github.pr.list/v1","reason":"List open PRs"}],"gaps":[]}`}
	p, err := suggestMarkdownTools(t.Context(), a.Path, "org/repo", "", e)
	if err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || p.Repository != "org/repo" || p.AgentDigest != a.Revision {
		t.Fatalf("%+v", p)
	}
	read, err := ReadMarkdownToolProposal(p.ID)
	if err != nil || read.ID != p.ID {
		t.Fatal(err)
	}
	if g, _, err := readCurrentToolGrant(a.Path); err != nil || g != nil {
		t.Fatal("suggestion granted access")
	}
	b, _ := os.ReadFile(a.Path)
	if !bytes.Equal(b, a.Source) {
		t.Fatal("suggestion edited source")
	}
	for _, repo := range []string{"org/*", "https://github.com/org/repo", "org/repo/../../other", "org/repo?x=1"} {
		before := e.calls
		if _, err = suggestMarkdownTools(t.Context(), a.Path, repo, "", e); err == nil || e.calls != before {
			t.Fatal("invalid scope reached inference")
		}
	}
}

func TestToolSuggestionSchemaIsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"template":"shell/v1","reason":"do it"}],"gaps":[]}`,
		`{"choices":[{"template":"github.pr.list/v1","reason":"ok","command":"gh api /user"}],"gaps":[]}`,
		`{"choices":[],"choices":[],"gaps":[]}`,
		`{"choices":[],"gaps":[],"repository":"other/repo"}`,
		"```json\n{}\n```",
	} {
		if _, _, err := decodeToolSuggestions(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func toolProposalFixture(t *testing.T) (*MarkdownAgent, MarkdownToolProposal, MarkdownToolGrant) {
	t.Helper()
	a := mdFixture(t)
	dir := t.TempDir()
	fakeTool(t, dir, "gh", "exit 0")
	t.Setenv("PATH", dir)
	p := MarkdownToolProposal{SchemaVersion: 1, ID: "p-" + strings.Repeat("a", 32), Source: a.Path, AgentDigest: a.Revision, Repository: "org/repo", Choices: []MarkdownToolChoice{{Template: "github.pr.list/v1", Reason: "read PRs"}, {Template: "github.pr.read/v1", Reason: "read checks"}}, Gaps: []string{}}
	g, err := PrepareToolGrant(p, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, g
}

func TestToolApprovalBindsSourceScopeAndExecutable(t *testing.T) {
	a, p, g := toolProposalFixture(t)
	if err := ApplyToolGrant(p, g); err != nil {
		t.Fatal(err)
	}
	if err := attachApprovedTools(a); err != nil || a.BindingDigest == "" || a.Grant.Repository != "org/repo" {
		t.Fatalf("attach: %v %+v", err, a.Grant)
	}
	frozen, err := CacheMarkdownVersion(a)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.BindingDigest != a.BindingDigest || frozen.Grant == nil {
		t.Fatal("cache dropped binding")
	}
	copy := *a
	copy.Grant = nil
	if err = loadToolSnapshot(&copy, a.BindingDigest); err != nil {
		t.Fatal(err)
	}
	if err = ApplyToolGrant(p, g); err == nil {
		t.Fatal("stale base grant accepted")
	}
	if err = os.WriteFile(a.Path, append(bytes.Clone(a.Source), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := ReadMarkdownAgent(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = attachApprovedTools(changed); err == nil {
		t.Fatal("changed definition inherited grant")
	}
	if err = os.WriteFile(g.Executable, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = executeGithubRead(t.Context(), g, "github_pr_list", map[string]any{}); err == nil {
		t.Fatal("changed executable ran")
	}
}

type githubFixtureTransport func(*http.Request) (*http.Response, error)

func (f githubFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubReadUsesOnlyBoundRepositoryGETs(t *testing.T) {
	a, p, g := toolProposalFixture(t)
	_ = a
	_ = p
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("KMX_GH_TEST_LOG", log)
	t.Setenv("GH_HOST", "evil.example")
	t.Setenv("GH_REPO", "other/repo")
	t.Setenv("GH_TOKEN", "ambient-auth-value")
	script := `printf '%s\n' "$PWD" "$GH_HOST" "${GH_REPO-unset}" "${GH_TOKEN-unset}" "$@" >> "$KMX_GH_TEST_LOG"
printf '%s' 'fixture-auth-value'`
	fakeTool(t, filepath.Dir(g.Executable), "gh", script)
	var err error
	g.ExecutableDigest, err = executableDigest(g.Executable)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	transport := githubFixtureTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer fixture-auth-value" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		var body string
		switch r.URL.RequestURI() {
		case "/repos/org/repo/pulls?state=open&per_page=20":
			body = `[{"number":42,"title":"Fix tests","state":"open","body":"not projected"}]`
		case "/repos/org/repo/pulls/42":
			body = `{"number":42,"title":"Fix tests","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"body":"not projected"}`
		case "/repos/org/repo/pulls/42/reviews?per_page=20":
			body = `[{"state":"APPROVED","body":"not projected"}]`
		case "/repos/org/repo/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs?per_page=20":
			body = `{"total_count":1,"check_runs":[{"name":"unit","status":"completed","conclusion":"failure","output":{"text":"not projected"}}]}`
		default:
			t.Fatalf("foreign request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	for _, name := range g.Tools {
		args := map[string]any{}
		if name == "github_pr_read" {
			args["number"] = 42
		}
		b, err := executeGithubReadTransport(t.Context(), g, name, args, transport)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) || strings.Contains(string(b), "not projected") {
			t.Fatalf("bad projection: %s", b)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	if requests != 4 || strings.Contains(text, "ambient-auth-value") || strings.Contains(text, "evil.example") || strings.Contains(text, "other/repo") || strings.Count(text, "auth\ntoken\n--hostname\ngithub.com\n") != 2 || !strings.Contains(text, "github.com\nunset\nunset\n") {
		t.Fatal(text)
	}
	before := text
	for _, args := range []any{map[string]any{"number": 42, "repository": "other/repo"}, map[string]any{"number": "--help"}, map[string]any{"number": 0}, map[string]any{"number": 1.5}, map[string]any{"number": 42, "command": "rm"}} {
		if _, err := executeGithubRead(t.Context(), g, "github_pr_read", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	after, _ := os.ReadFile(log)
	if string(after) != before {
		t.Fatal("invalid arguments executed gh")
	}
}

func TestMockTestCannotFallThroughToApprovedLiveTool(t *testing.T) {
	a, p, g := toolProposalFixture(t)
	if err := ApplyToolGrant(p, g); err != nil {
		t.Fatal(err)
	}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.Provider = "copilot"
	e := &mdFakeExecutor{answer: "hello"}
	s.executor = e
	if _, err := s.Evaluate(t.Context(), a, "", nil); err == nil || e.starts != 0 || e.calls != 0 {
		t.Fatal("missing mock coverage reached execution")
	}
}

func TestGitHubReadRefusesRedirectsAndBoundsResponses(t *testing.T) {
	_, _, g := toolProposalFixture(t)
	fakeTool(t, filepath.Dir(g.Executable), "gh", `printf '%s' 'fixture-auth-value'`)
	g.ExecutableDigest, _ = executableDigest(g.Executable)
	for _, status := range []int{302, 403, 200} {
		calls := 0
		transport := githubFixtureTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			header := http.Header{}
			header.Set("Location", "https://api.github.com/repos/other/repo/pulls")
			body := "[]"
			if status == 200 {
				body = strings.Repeat("x", (64<<10)+1)
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, err := executeGithubReadTransport(t.Context(), g, "github_pr_list", map[string]any{}, transport); err == nil || calls != 1 {
			t.Fatalf("status=%d calls=%d err=%v", status, calls, err)
		}
	}
}

func TestBindingOnlyUpdateReplacesSessions(t *testing.T) {
	a, p, g := toolProposalFixture(t)
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "shared", newExecutor: func() markdownExecutor { return &mdFakeExecutor{} }}
	defer m.close()
	started := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision})
	if started.Instance == nil {
		t.Fatal(started.Error)
	}
	r := *started.Instance
	if err := ApplyToolGrant(p, g); err != nil {
		t.Fatal(err)
	}
	grant, digest, err := readCurrentToolGrant(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	updated := m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "update", NewVersion: a.Revision, BindingDigest: digest})
	if updated.Instance == nil || updated.Instance.Generation != r.Generation+1 || updated.Instance.Version != r.Version || updated.Instance.BindingDigest != digest {
		t.Fatalf("binding-only update: %+v", updated)
	}
	if m.agents[r.ID].definition.Grant.Repository != grant.Repository {
		t.Fatal("frozen grant missing")
	}
}
