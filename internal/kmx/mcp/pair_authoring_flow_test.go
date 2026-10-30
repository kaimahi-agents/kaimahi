//go:build linux || darwin

package mcp

import (
	"bytes"
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
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

func TestPairAuthoringRestorePreservesCurrentSelectorIntent(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	inline := pendingRecordingSource(t)
	without := strings.Split(string(inline), "## Examples")[0]
	if _, err := f.Create(t.Context(), "a.agent.md", inline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.agent.test.md"), []byte(pairPendingTests), 0600); err != nil {
		t.Fatal(err)
	}
	version := pairContextRead(t, cs, "a.agent.md", "a.agent.test.md")
	pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "tests": "a.agent.test.md", "expectedSourceVersion": version, "content": without, "acknowledgeCopilotTests": true})
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(view.Contents[0].Text), &d) != nil || d.Validation != "valid" || len(d.History.Versions) != 1 {
		t.Fatal("wrong setup")
	}
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "expectedSourceVersion": d.SourceVersion, "restoreVersionId": d.History.Versions[0].VersionID, "acknowledgeCopilotTests": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("historical explicit intent overrode current default selection", result.StructuredContent)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != without {
		t.Fatal("invalid current-default candidate was published")
	}
	// The same old bytes are valid when the caller really selects explicitly.
	pairSaveCall(t, cs, "edit", map[string]any{"path": "a.agent.md", "tests": "a.agent.test.md", "expectedSourceVersion": pairContextRead(t, cs, "a.agent.md", "a.agent.test.md"), "restoreVersionId": d.History.Versions[0].VersionID, "acknowledgeCopilotTests": true})
	if got, _ := os.ReadFile(filepath.Join(root, "a.agent.md")); string(got) != string(inline) {
		t.Fatal("explicit restore lost original bytes")
	}
}

func TestPairAuthoringRestoreCapabilityNeedsStore(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	f.historyDir = nil
	cs := historyClient(t, f)
	if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
		t.Fatal(err)
	}
	result, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if err != nil {
		t.Fatal(err)
	}
	var d sourceDocument
	if json.Unmarshal([]byte(result.Contents[0].Text), &d) != nil || d.PairOperations.Restore || !d.PairOperations.Edit {
		t.Fatal("restore advertised without a store")
	}
}

func TestPairAuthoringOptionalHistoryCannotHideNearLimitSources(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	if _, e := f.Create(t.Context(), "a.agent.md", []byte("Before\n")); e != nil {
		t.Fatal(e)
	}
	version := pairContextRead(t, cs, "a.agent.md", "")
	physical := filepath.Join(root, "a.agent.md")
	testsPath := filepath.Join(root, "a.agent.test.md")
	makeSources := func(n int) (string, string, int) {
		source := strings.Repeat("<", n)
		tests := "---\nschemaVersion: 1\n---\n" + strings.Repeat("<", n) + "\n"
		a, e := agentfile.Parse(physical, []byte(source))
		if e != nil {
			t.Fatal(e)
		}
		p, e := agentfile.ComposePair(physical, a, agentfile.TestsSelection{Path: testsPath}, []byte(tests), true)
		if e != nil {
			t.Fatal(e)
		}
		view := pairDocument(p, []byte(source))
		view.SourceVersion = strings.Repeat("x", 128)
		view.Validation = "valid"
		view.PairOperations = &pairOperationsCapability{SchemaVersion: 1, ToolSchemaVersion: 3, Execute: true, Create: true, Edit: true, Capture: true, Restore: true}
		b, e := json.Marshal(view)
		if e != nil {
			t.Fatal(e)
		}
		return source, tests, len(b)
	}
	low, high := 1, 200000
	for low < high {
		mid := (low + high + 1) / 2
		_, _, size := makeSources(mid)
		if size <= (2<<20)-256 {
			low = mid
		} else {
			high = mid - 1
		}
	}
	source, tests, preflight := makeSources(low)
	args := map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": source, "testsContent": tests, "expectedSourceVersion": version}
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	enc.SetEscapeHTML(false)
	if e := enc.Encode(args); e != nil {
		t.Fatal(e)
	}
	result, e := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var ack pairSaveResult
	json.Unmarshal(raw, &ack)
	view, e := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI("a.agent.md", "")})
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]any
	json.Unmarshal([]byte(view.Contents[0].Text), &body)
	a, _ := os.ReadFile(physical)
	b, _ := os.ReadFile(testsPath)
	bound, e := f.bindPair(t.Context(), "a.agent.md", "")
	if e != nil {
		t.Fatal(e)
	}
	defer bound.close()
	actual := sourcesDocument(bound.sources, bound.agent.source)
	actual.Validation = "valid"
	actual.SourceVersion = bound.sourceVersion()
	actual.PairOperations = &pairOperationsCapability{SchemaVersion: 1, ToolSchemaVersion: 3, Execute: true, Create: true, Edit: true, Capture: true, Restore: true}
	actual.History = f.historyView(t.Context(), "a.agent.md")
	for i := range actual.History.Versions {
		actual.History.Versions[i].Label = pairChangeLabel(actual.History.Versions[i], bound.pair)
	}
	expanded, e := json.Marshal(actual)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("actualCompleteView=%d historyRows=%d", len(expanded), len(actual.History.Versions))
	t.Logf("inboundUnescaped=%d sourceBytes=%d/%d preflightView=%d limit=%d ACK=%s afterKind=%v afterReason=%v exactEffects=%v/%v", input.Len(), len(source), len(tests), preflight, 2<<20, raw, body["kind"], body["reasonCode"], string(a) == source, string(b) == tests)
	if ack.State == "complete" && body["kind"] != "kmx.agent-sources" {
		t.Fatal("complete edit published a pair that the mandatory resource cannot return")
	}
}

func TestPairAuthoringSourceAndEnvelopeBudgets(t *testing.T) {
	for _, kind := range []string{"exact-source-limit", "source-over-limit", "escaped-envelope-limit", "escaped-view-limit"} {
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
			if kind == "escaped-view-limit" {
				agent = strings.Repeat("<", 230000)
				tests = "---\nschemaVersion: 1\n---\n" + strings.Repeat("<", 230000) + "\n"
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
