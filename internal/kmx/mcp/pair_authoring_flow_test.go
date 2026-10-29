//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func pairSaveCall(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) pairSaveResult {
	t.Helper()
	args["schemaVersion"] = 3
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var ack pairSaveResult
	if json.Unmarshal(data, &ack) != nil || result.IsError || ack.State != "complete" || ack.SourceVersion == "" {
		t.Fatalf("pair save: %s", data)
	}
	return ack
}
func TestPairAuthoringAbsenceRestoreAndTwoRoleEdit(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	agent := []byte("Prompt\n")
	if _, err := f.Create(t.Context(), "a.agent.md", agent); err != nil {
		t.Fatal(err)
	}
	version := pairContextRead(t, cs, "a.agent.md", "")
	first := pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "expectedSourceVersion": version, "testsContent": historyTests})
	if first.Agent.Status != "unchanged" || first.Tests.Status != "saved" {
		t.Fatal("wrong created role", first)
	}
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(view.Contents[0].Text), &d) != nil || d.History == nil || len(d.History.Versions) != 1 || d.History.Versions[0].TestsState != "absent" {
		t.Fatal("absence history missing")
	}
	second := pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "tests": "a.agent.test.md", "expectedSourceVersion": first.SourceVersion, "restoreVersionId": d.History.Versions[0].VersionID})
	if second.Agent.Status != "unchanged" || second.Tests.Status != "saved" {
		t.Fatal("wrong removal role")
	}
	if _, err := os.Lstat(filepath.Join(root, "a.agent.test.md")); !os.IsNotExist(err) {
		t.Fatal("absence not restored", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != string(agent) {
		t.Fatal("absence restore changed prompt")
	}
	third := pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "expectedSourceVersion": pairContextRead(t, cs, "a.agent.md", ""), "content": "Changed prompt\n", "testsContent": historyTests + "\nChanged suite\n"})
	if third.Agent.Status != "saved" || third.Tests.Status != "saved" {
		t.Fatal("two-role save not complete", third)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != "Changed prompt\n" {
		t.Fatal("agent save lost")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.test.md")); string(got) != historyTests+"\nChanged suite\n" {
		t.Fatal("tests save lost")
	}
}
func TestPairAuthoringRepairHasNoCorruptHistory(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	source := []byte(strings.Split(string(pendingRecordingSource(t)), "## Examples")[0])
	if _, err := f.Create(t.Context(), "a.agent.md", source); err != nil {
		t.Fatal(err)
	}
	tests := strings.ReplaceAll(pairPendingTests, "ping", "other")
	if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(tests), 0600); err != nil {
		t.Fatal(err)
	}
	version := pairContextRead(t, cs, "a.agent.md", "")
	ack := pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "expectedSourceVersion": version, "content": strings.ReplaceAll(string(source), "ping", "other")})
	if ack.History == nil || ack.History.Status != "unavailable" || ack.Agent.Status != "saved" || ack.Tests.Status != "unchanged" {
		t.Fatal("repair fabricated history", ack)
	}
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(view.Contents[0].Text), &d) != nil || d.Validation != "valid" || d.History == nil || len(d.History.Versions) != 0 {
		t.Fatal("repair retained corrupt preimage", view)
	}
}
func TestPairAuthoringRootlessRepositoryCreate(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	cs := historyClient(t, f)
	agent := filepath.Join(root, "new repo", ".github", "agents", "a.agent.md")
	ack := pairSaveCall(t, cs, "create", map[string]any{"path": agent, "content": "Prompt\n", "testsContent": historyTests})
	if ack.History != nil || ack.Agent.Status != "saved" || ack.Tests.Status != "saved" {
		t.Fatal("create invented preimage", ack)
	}
	tests := filepath.Join(root, "new repo", ".github", "agent-tests", "a.agent.test.md")
	if got, _ := os.ReadFile(tests); string(got) != historyTests {
		t.Fatal("repository suite missing")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(agent), "a.agent.test.md")); !os.IsNotExist(err) {
		t.Fatal("created wrong default")
	}
}

func TestPairAuthoringSourceAndEnvelopeBudgets(t *testing.T) {
	for _, kind := range []string{"exact-source-limit", "source-over-limit", "escaped-envelope-limit"} {
		t.Run(kind, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			cs := historyClient(t, f)
			agent := strings.Repeat("x", maxFileBytes)
			tests := historyTests + strings.Repeat("x", maxFileBytes-len(historyTests))
			if kind == "source-over-limit" {
				tests += "x"
			}
			if kind == "escaped-envelope-limit" {
				agent = strings.Repeat("\x01", 230000)
				tests = "---\nschemaVersion: 1\n---\n" + strings.Repeat("\x01", 230000) + "\n"
			}
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": agent, "testsContent": tests}})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "exact-source-limit" {
				if result.IsError {
					t.Fatalf("source limit refused: %+v", result.StructuredContent)
				}
				for p, want := range map[string]string{"a.agent.md": agent, "a.agent.test.md": tests} {
					got, _ := os.ReadFile(filepath.Join(root, p))
					if string(got) != want {
						t.Fatal("limit source truncated")
					}
				}
			} else {
				if !result.IsError {
					t.Fatal("oversize accepted")
				}
				for _, p := range []string{"a.agent.md", "a.agent.test.md"} {
					if _, err := os.Lstat(filepath.Join(root, p)); !os.IsNotExist(err) {
						t.Fatal("oversize wrote a source")
					}
				}
			}
		})
	}
}

func TestPairAuthoringRequestRefusalsHaveZeroSourceEffects(t *testing.T) {
	for _, kind := range []string{"missing-agent-content", "missing-tests-content", "create-selector", "invalid-tests", "invalid-agent", "missing-explicit", "stale-context", "wrong-root", "existing-tests"} {
		t.Run(kind, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			cs := historyClient(t, f)
			args := map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": "Prompt\n", "testsContent": historyTests}
			name := "create"
			switch kind {
			case "missing-agent-content":
				delete(args, "content")
			case "missing-tests-content":
				delete(args, "testsContent")
			case "create-selector":
				args["tests"] = "chosen.md"
			case "invalid-tests":
				args["testsContent"] = "not a typed tests container"
			case "invalid-agent":
				args["content"] = ""
			case "wrong-root":
				args["path"] = filepath.Join(t.TempDir(), "a.agent.md")
			case "existing-tests":
				if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte("untouched sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-explicit", "stale-context":
				if _, err := f.Create(t.Context(), "a.agent.md", []byte("Original\n")); err != nil {
					t.Fatal(err)
				}
				args = map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": "Changed\n", "expectedSourceVersion": pairContextRead(t, cs, "a.agent.md", "")}
				name = "edit"
				if kind == "missing-explicit" {
					args["tests"] = filepath.Join(root, "private", "authorized_keys")
					args["testsContent"] = historyTests
				} else {
					args["expectedSourceVersion"] = "not-issued"
				}
			}
			before := map[string]string{}
			filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
				if e == nil && !d.IsDir() {
					b, _ := os.ReadFile(p)
					before[p] = string(b)
				}
				return e
			})
			result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Fatal("accepted invalid request", kind)
			}
			after := map[string]string{}
			filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
				if e == nil && !d.IsDir() {
					b, _ := os.ReadFile(p)
					after[p] = string(b)
				}
				return e
			})
			if len(before) != len(after) {
				t.Fatal("refusal wrote a source")
			}
			for p, want := range before {
				if after[p] != want {
					t.Fatal("refusal changed source", p)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "private")); !os.IsNotExist(err) {
				t.Fatal("missing explicit selection created parent")
			}
		})
	}
}
