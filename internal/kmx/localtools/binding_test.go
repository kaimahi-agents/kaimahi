package localtools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const bindingJSON = `{"schemaVersion":"kmx.local-tools/v1","bindings":[{"name":"github_list_merged_prs","endpointRef":"github","repository":"owner/repo"}],"endpoints":{"github":{"url":"https://api.githubcopilot.com/mcp/readonly","credentialEnv":"GITHUB_TOKEN"}}}`

func testBinding(t *testing.T) Binding {
	t.Helper()
	b, err := Parse([]byte(bindingJSON))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBindingClosedShape(t *testing.T) {
	b := testBinding(t)
	if !b.Matches([]string{"github_list_merged_prs"}) || b.Matches(nil) || b.Matches([]string{"other"}) || b.Matches([]string{Operation, Operation}) || (Binding{}).Matches([]string{Operation}) {
		t.Fatal("binding matched wrong declaration")
	}
	if b.Repository() != "owner/repo" || b.Endpoint() != "https://api.githubcopilot.com/mcp/readonly" || b.CredentialEnv() != "GITHUB_TOKEN" {
		t.Fatal("binding changed authored selection")
	}
	data, err := json.Marshal(struct {
		Binding any `json:"binding"`
	}{b})
	if err != nil || string(data) != `{"binding":{}}` {
		t.Fatal("binding fields are public")
	}
	for _, input := range []string{
		"", "null", "[]", bindingJSON + "{}", strings.Repeat(" ", 16385),
		strings.Replace(bindingJSON, `"schemaVersion":`, `"extra":true,"schemaVersion":`, 1),
		strings.Replace(bindingJSON, `"schemaVersion":`, `"schemaVersion":"other","schemaVersion":`, 1),
		strings.Replace(bindingJSON, `"name":`, `"Name":"github_list_merged_prs","name":`, 1),
		strings.Replace(bindingJSON, `"repository":`, `"secret":"value","repository":`, 1),
		strings.Replace(bindingJSON, `"url":`, `"url":"bad","url":`, 1),
		strings.Replace(bindingJSON, `"endpoints":`, `"endpoints":{},"endpoints":`, 1),
		strings.Replace(bindingJSON, `"github":{`, `"extra":{},"github":{`, 1),
		strings.Replace(bindingJSON, `"bindings":[{`, `"bindings":[{}, {`, 1),
		strings.Replace(bindingJSON, `"credentialEnv":"GITHUB_TOKEN"`, `"credentialEnv":null`, 1),
		strings.Replace(bindingJSON, `"credentialEnv":"GITHUB_TOKEN"`, `"credentialEnv":12`, 1),
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("accepted invalid shape: %q", input)
		}
	}
	for _, row := range []struct {
		old string
		bad []string
	}{
		{"kmx.local-tools/v1", []string{"", "other"}},
		{"github_list_merged_prs", []string{"", "other"}},
		{"owner/repo", []string{"", "owner", "owner/repo/extra", "../repo", "owner/..", "owner/r?", "-owner/repo", "owner-/repo"}},
		{"GITHUB_TOKEN", []string{"", "value with spaces", "1TOKEN", "ghp_" + strings.Repeat("a", 36)}},
		{"https://api.githubcopilot.com/mcp/readonly", []string{"", "https://api.githubcopilot.com/mcp/readonly/", "http://api.githubcopilot.com/mcp/readonly", "https://evil.test/mcp/readonly", "https://api.githubcopilot.com/mcp/readonly?x=y"}},
		{"github", []string{"", "../github", "github/other", "ghp_" + strings.Repeat("a", 36)}},
	} {
		for _, bad := range row.bad {
			if _, err := Parse([]byte(strings.ReplaceAll(bindingJSON, row.old, bad))); err == nil {
				t.Fatalf("accepted invalid %s", row.old)
			}
		}
	}
	for _, field := range []string{`"schemaVersion":"kmx.local-tools/v1",`, `"name":"github_list_merged_prs",`, `"endpointRef":"github",`, `,"repository":"owner/repo"`, `"url":"https://api.githubcopilot.com/mcp/readonly",`, `,"credentialEnv":"GITHUB_TOKEN"`} {
		if _, err := Parse([]byte(strings.Replace(bindingJSON, field, "", 1))); err == nil {
			t.Fatal("accepted missing field")
		}
	}
}

func TestBindingLoadBoundedCheckedPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "binding.json")
	if err := os.WriteFile(path, []byte(bindingJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := Load(path); err != nil || !b.Matches([]string{Operation}) {
		t.Fatal("valid load refused", err)
	}
	for _, name := range []string{"", "../binding.json", dir, filepath.Join(dir, "missing"), dir + "/../" + filepath.Base(dir) + "/binding.json"} {
		if _, err := Load(name); err == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		parentLink := filepath.Join(dir, "parent")
		if err := os.Symlink(dir, parentLink); err != nil {
			t.Fatal(err)
		}
		fifo := filepath.Join(dir, "fifo")
		if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo: %v %s", err, out)
		}
		for _, name := range []string{link, filepath.Join(parentLink, "binding.json"), fifo} {
			if _, err := Load(name); err == nil {
				t.Fatal("special/link path accepted")
			}
		}
	}
	if err := os.WriteFile(path, []byte(bindingJSON+strings.Repeat(" ", 16384)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversized file accepted")
	}
}
