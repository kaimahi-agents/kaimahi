//go:build linux || darwin

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func historyFiles(t *testing.T, root, base string) *Files {
	t.Helper()
	f, err := OpenFilesWithHistory(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err }, func() (string, error) { return base, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func historyClient(t *testing.T, f *Files) *sdk.ClientSession {
	t.Helper()
	return connectFixture(t, Operations(f, &app.App{}, nil), func(s *sdk.Server) { AddAgentResource(s, f); AddAgentVersionResource(s, f) })
}

func historyEdit(t *testing.T, cs *sdk.ClientSession, p, expected, source string) (File, *sdk.CallToolResult) {
	t.Helper()
	r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 1, "path": p, "expectedRevision": expected, "content": source}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r.StructuredContent)
	var result struct{ File File }
	if json.Unmarshal(b, &result) != nil {
		t.Fatal("invalid edit result")
	}
	return result.File, r
}

func requireHistoryToolReason(t *testing.T, result *sdk.CallToolResult, code string) {
	t.Helper()
	b, _ := json.Marshal(result.StructuredContent)
	var body struct {
		ReasonCode string `json:"reasonCode"`
	}
	if json.Unmarshal(b, &body) != nil || !result.IsError || body.ReasonCode != code {
		t.Fatalf("tool refusal = %+v; want %s", result, code)
	}
}

func TestHistoryUnboundCanonicalTargetsAndRestore(t *testing.T) {
	for _, suffix := range []string{"greeter.agent.md", ".squad/agents/greeter/charter.md"} {
		t.Run(suffix, func(t *testing.T) {
			_, _, source := canonicalFiles(t)
			base, _ := historyPaths(t)
			workspace := t.TempDir()
			t.Chdir(workspace)
			repo := filepath.Join(workspace, "Repo One")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			legacy := historyFiles(t, repo, base)
			original, err := legacy.Create(t.Context(), suffix, []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			second := strings.ReplaceAll(source, "Hello", "Welcome")
			current, err := legacy.Edit(t.Context(), suffix, original.Revision, []byte(second))
			if err != nil {
				t.Fatal(err)
			}
			prior := legacy.historyView(t.Context(), suffix).Versions[0]
			target := filepath.Join(repo, filepath.FromSlash(suffix))
			alias := "Agent alias.agent.md"
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			f := historyFiles(t, "", base)
			cs := historyClient(t, f)
			view := readView(t, cs, alias)
			if view.File.Path != alias || view.History.Status != "available" || len(view.History.Versions) != 1 || view.History.Versions[0].VersionID != prior.VersionID {
				t.Fatalf("canonical target did not share legacy history: %+v", view)
			}
			saved, result := historyEdit(t, cs, alias, current.Revision, strings.ReplaceAll(source, "Hello", "Howdy"))
			if result.IsError || saved.Path != alias || saved.History == nil || saved.History.Status != "retained" {
				t.Fatalf("unbound alias save: %+v / %+v", saved, result)
			}
			view = readView(t, cs, target)
			if view.File.Path != target || len(view.History.Versions) != 2 {
				t.Fatal("absolute target did not share alias history")
			}
			old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: view.History.Versions[1].URI})
			if err != nil {
				t.Fatal("absolute version read failed", err)
			}
			var document struct{ Source string }
			if json.Unmarshal([]byte(old.Contents[0].Text), &document) != nil || document.Source != source {
				t.Fatal("old source bytes changed")
			}
			restored, result := historyEdit(t, cs, alias, saved.Revision, document.Source)
			if result.IsError || restored.Path != alias || restored.Revision != original.Revision || restored.History.Status != "retained" {
				t.Fatal("unbound restore did not use ordinary Edit")
			}
			if rows := legacy.historyView(t.Context(), suffix).Versions; len(rows) != 3 {
				t.Fatal("legacy view lost unbound restore/undo history")
			}
			other := filepath.Join(workspace, "Repo Two", "greeter.agent.md")
			if _, err := f.Create(t.Context(), other, []byte(source)); err != nil {
				t.Fatal(err)
			}
			if h := f.historyView(t.Context(), other); h.Status != "available" || len(h.Versions) != 0 {
				t.Fatal("history crossed repository targets")
			}
			if err := legacy.ClearHistory(t.Context()); err != nil {
				t.Fatal(err)
			}
			if h := f.historyView(t.Context(), target); len(h.Versions) != 0 {
				t.Fatal("legacy root clear did not clear canonical target history")
			}
		})
	}
}

func TestHistoryVersionResourceReservedPathCharacters(t *testing.T) {
	_, _, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	cs := historyClient(t, historyFiles(t, "", base))
	requested := filepath.Join(t.TempDir(), "team@x.agent.md")
	saved := authoringCall(t, cs, "create", requested, source, "")
	authoringCall(t, cs, "edit", requested, source+"\n", saved.Revision)
	view := readView(t, cs, requested)
	old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: view.History.Versions[0].URI})
	if err != nil {
		t.Fatal("reserved path character must match the version resource template", err)
	}
	var document struct{ Source string }
	if json.Unmarshal([]byte(old.Contents[0].Text), &document) != nil || document.Source != source {
		t.Fatal("version source bytes changed")
	}
}

func TestHistoryMCPRestoreUndoAndRestart(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	source = strings.ReplaceAll(source, "\n", "\r\n")
	saved, err := f.Create(t.Context(), ".squad/agents/greeter/charter.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, filepath.FromSlash(saved.Path)), 0640); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	if view := readView(t, cs, saved.Path); view.History == nil || view.History.Status != "available" || len(view.History.Versions) != 0 {
		t.Fatalf("empty history = %+v", view.History)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("read created history")
	}
	second := strings.ReplaceAll(source, "Hello", "Welcome")
	file, result := historyEdit(t, cs, filepath.Join(root, filepath.FromSlash(saved.Path)), saved.Revision, second)
	if result.IsError || file.Revision != revision([]byte(second)) || file.History == nil || file.History.Status != "retained" {
		t.Fatalf("save = %+v / %+v", file, result)
	}
	view := readView(t, cs, saved.Path)
	if len(view.History.Versions) != 1 || view.History.Versions[0].Revision != saved.Revision || view.History.Versions[0].SourceBytes != len(source) {
		t.Fatalf("prior version missing: %+v", view.History)
	}
	version := view.History.Versions[0]
	old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: version.URI})
	if err != nil || len(old.Contents) != 1 {
		t.Fatal("version read failed", err)
	}
	var document struct {
		Source string
		File   File
	}
	if json.Unmarshal([]byte(old.Contents[0].Text), &document) != nil || document.Source != source || document.File.Revision != saved.Revision {
		t.Fatal("restorable bytes changed")
	}
	restored, result := historyEdit(t, cs, saved.Path, file.Revision, document.Source)
	if result.IsError || restored.Revision != saved.Revision || restored.History.Status != "retained" {
		t.Fatalf("restore failed: %+v / %+v", restored, result)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(saved.Path)))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("restore changed source mode", err)
	}
	view = readView(t, cs, saved.Path)
	if len(view.History.Versions) != 2 || view.History.Versions[0].Revision != file.Revision || view.History.Versions[1].VersionID != version.VersionID {
		t.Fatal("restore rewrote history instead of retaining undo")
	}
	// Existing Edit also performs undo; another A occurrence must not overwrite A.
	_, result = historyEdit(t, cs, saved.Path, restored.Revision, second)
	if result.IsError {
		t.Fatal("undo failed")
	}
	reopened := historyFiles(t, root, base)
	fresh := historyClient(t, reopened)
	view = readView(t, fresh, saved.Path)
	if len(view.History.Versions) != 3 || view.History.Versions[0].Revision != saved.Revision || view.History.Versions[0].VersionID == version.VersionID || view.Latest.Result != nil {
		t.Fatalf("restart/occurrence contract lost: %+v", view)
	}
	tools, err := fresh.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatal("history added a tool", err)
	}
	_, stale := historyEdit(t, fresh, saved.Path, saved.Revision, source)
	requireHistoryToolReason(t, stale, "stale_revision")
	if err := reopened.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: version.URI}); err == nil || !strings.Contains(err.Error(), "protocol_failed") {
		t.Fatalf("cleared version did not retain generic resource refusal: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(saved.Path)))
	if string(b) != second {
		t.Fatal("clear modified current source")
	}
}

func TestHistoryZeroExampleSourceRestoresThroughEdit(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	zero := strings.Split(source, "```yaml")[0] + "```yaml\nexamples: []\n```\n"
	original, err := f.Create(t.Context(), "greeter.agent.md", []byte(zero))
	if err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	next, result := historyEdit(t, cs, original.Path, original.Revision, strings.ReplaceAll(zero, "Hello", "Welcome"))
	if result.IsError || next.History == nil || next.History.Status != "retained" {
		t.Fatalf("zero-example save lost history: %+v / %+v", next, result)
	}
	view := readView(t, cs, original.Path)
	if len(view.Examples) != 0 || !view.Authoring.Editable || len(view.History.Versions) != 1 {
		t.Fatal("zero-example canonical source is not editable with history")
	}
	old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: view.History.Versions[0].URI})
	if err != nil {
		t.Fatal(err)
	}
	var document struct{ Source string }
	if json.Unmarshal([]byte(old.Contents[0].Text), &document) != nil || document.Source != zero {
		t.Fatal("zero-example source bytes changed")
	}
	restored, result := historyEdit(t, cs, original.Path, next.Revision, document.Source)
	if result.IsError || restored.Revision != original.Revision || restored.History.Status != "retained" {
		t.Fatal("zero-example restore bypassed ordinary Edit or failed")
	}
	view = readView(t, cs, original.Path)
	if len(view.History.Versions) != 2 || view.Latest.Result != nil {
		t.Fatal("restore lost undo or fabricated execution evidence")
	}
}

func TestHistoryFailureStillSavesAndWarnsThroughMCP(t *testing.T) {
	for _, mode := range []string{"busy", "permissions", "oversize", "resolver"} {
		t.Run(mode, func(t *testing.T) {
			_, root, source := canonicalFiles(t)
			base, _ := historyPaths(t)
			f := historyFiles(t, root, base)
			saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			store := historyStoreForTest(t, base, f.root.Name(), true)
			if err := store.retain(t.Context(), saved.Path, []byte(source)); err != nil {
				t.Fatal(err)
			}
			if mode != "busy" {
				rows, _ := store.list(saved.Path)
				if mode == "oversize" {
					if err := os.Truncate(filepath.Join(store.root.Name(), rows[0].VersionID+".json"), maxHistoryRecordBytes+1); err != nil {
						t.Fatal(err)
					}
				}
				store.close()
			}
			if mode == "permissions" {
				if err := os.Chmod(base, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "resolver" {
				f.historyDir = func() (string, error) { return "", errors.New("PRIVATE OS ERROR") }
			}
			cs := historyClient(t, f)
			second := strings.ReplaceAll(source, "Hello", "Welcome")
			file, result := historyEdit(t, cs, saved.Path, saved.Revision, second)
			encoded, _ := json.Marshal(result)
			if result.IsError || file.Revision != revision([]byte(second)) || file.History == nil || file.History.Status != "unavailable" || file.History.ReasonCode != "history_unavailable" || !strings.Contains(result.Content[0].(*sdk.TextContent).Text, "Saved") || strings.Contains(string(encoded), "PRIVATE OS ERROR") {
				t.Fatalf("history blocked/silently degraded save: %+v / %+v", file, result)
			}
			b, _ := os.ReadFile(filepath.Join(root, saved.Path))
			if string(b) != second {
				t.Fatal("source not saved despite success")
			}
			view := readView(t, cs, saved.Path)
			if view.Authoring.Source != second || view.History.Status != "unavailable" {
				t.Fatal("history failure blocked ordinary current view")
			}
		})
	}
}

func TestHistoryDamagedSnapshotDoesNotHideHealthyVersionsOrBlockEdit(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	var damagedURI, damagedPath string
	var currentB File
	for _, p := range []string{"a.agent.md", "b.agent.md"} {
		file, err := f.Create(t.Context(), p, []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		file, err = f.Edit(t.Context(), p, file.Revision, []byte(strings.ReplaceAll(source, "Hello", "Welcome")))
		if err != nil {
			t.Fatal(err)
		}
		if p == "b.agent.md" {
			currentB = file
		}
	}
	store := historyStoreForTest(t, base, f.root.Name(), false)
	rows, err := store.list("a.agent.md")
	if err != nil || len(rows) != 1 {
		t.Fatal("missing original snapshot", err)
	}
	damagedURI = agentVersionURI("a.agent.md", rows[0].VersionID)
	damagedPath = filepath.Join(store.root.Name(), rows[0].VersionID+".json")
	store.close()
	if err := os.WriteFile(damagedPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	view := readView(t, cs, currentB.Path)
	if view.History.Status != "available" || len(view.History.Versions) != 1 {
		t.Fatalf("damaged A hid B's healthy history: %+v", view.History)
	}
	healthyURI := view.History.Versions[0].URI
	old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: healthyURI})
	if err != nil {
		t.Fatal("damaged A blocked B read", err)
	}
	var document struct{ Source string }
	if json.Unmarshal([]byte(old.Contents[0].Text), &document) != nil || document.Source != source {
		t.Fatal("healthy source changed")
	}
	next, result := historyEdit(t, cs, currentB.Path, currentB.Revision, strings.ReplaceAll(source, "Hello", "Howdy"))
	if result.IsError || next.History == nil || next.History.Status != "retained" {
		t.Fatalf("damaged A prevented later Edit retention: %+v / %+v", next, result)
	}
	view = readView(t, cs, currentB.Path)
	if view.History.Status != "available" || len(view.History.Versions) != 2 || view.History.Versions[0].Revision != currentB.Revision {
		t.Fatal("later Edit did not retain B's displaced source")
	}
	if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: damagedURI}); err == nil {
		t.Fatal("damaged A version readable")
	}
}

func TestHistoryCancellationDoesNotClaimSaved(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.historyDir = func() (string, error) { cancel(); return base, nil }
	file, err := f.Edit(ctx, saved.Path, saved.Revision, []byte(strings.ReplaceAll(source, "Hello", "Welcome")))
	requireCode(t, err, "cancelled")
	if file.Revision != "" || file.History != nil {
		t.Fatal("cancelled source save claimed success")
	}
	b, _ := os.ReadFile(filepath.Join(root, saved.Path))
	if string(b) != source {
		t.Fatal("cancelled source changed")
	}
}

func TestHistorySameServerReadsDoNotLoseUndo(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	file, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if view := f.historyView(t.Context(), file.Path); view.Status != "available" {
					t.Error("same-server history read reported self-busy")
					return
				}
			}
		}
	}()
	defer func() { close(stop); reader.Wait() }()
	for i := 0; i < 22; i++ {
		next := strings.ReplaceAll(source, "Hello", "Welcome"+strings.Repeat("!", i))
		// The path is immutable; avoid sharing the changing revision with readers.
		saved, err := f.Edit(t.Context(), file.Path, file.Revision, []byte(next))
		if err != nil || saved.History == nil || saved.History.Status != "retained" {
			t.Fatalf("ordinary overlapping read lost undo: %+v / %v", saved, err)
		}
		file.Revision = saved.Revision
	}
	if view := f.historyView(t.Context(), file.Path); len(view.Versions) != 20 {
		t.Fatalf("normal growth did not evict to the per-file limit: %+v", view)
	}
}

func TestHistoryReadAuthorizationAndStrictRestore(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	file, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.Create(t.Context(), "other.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.ReplaceAll(source, "Hello", "Welcome")
	file, err = f.Edit(t.Context(), file.Path, file.Revision, []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	view := readView(t, cs, file.Path)
	version := view.History.Versions[0]
	for _, uri := range []string{
		agentVersionURI(other.Path, version.VersionID),
		"kmx://agent-version/..%2Fgreeter.agent.md/" + version.VersionID,
		"kmx://agent-version/greeter.agent.md/" + version.VersionID + "?source=true",
		"kmx://agent-version/greeter.agent.md/not-a-version",
	} {
		if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Fatalf("foreign/unsafe version readable: %s", uri)
		}
	}
	// A parser/validator upgrade may reject old content without hiding history.
	f.validate = func(p string, b []byte) error {
		if string(b) == source {
			return errors.New("old source no longer accepted")
		}
		_, err := agentfile.Parse(p, b)
		return err
	}
	_, result := historyEdit(t, cs, file.Path, file.Revision, source)
	requireHistoryToolReason(t, result, "invalid_file")
	// Minimal sources now support the same revision-checked restore as charters.
	minimal := "# Greeter\n\nGreet the supplied name.\n\n## Model\n- **Preferred:** fake\n\n## Examples" + strings.Split(source, "## Examples")[1]
	if err := os.WriteFile(filepath.Join(root, file.Path), []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	view = readView(t, cs, file.Path)
	if !view.Authoring.Editable || len(view.History.Versions) != 1 {
		t.Fatal("minimal current source hid readable history or editing")
	}
	_, result = historyEdit(t, cs, file.Path, revision([]byte(minimal)), second)
	if result.IsError {
		t.Fatalf("minimal source restore failed: %+v", result)
	}
	if err := os.Remove(filepath.Join(root, file.Path)); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: version.URI}); err == nil {
		t.Fatal("deleted current file still authorized version reads")
	}
}

func TestHistorySourcePublicationFailureDoesNotClaimSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	file, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	f.historyDir = func() (string, error) {
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
		return base, nil
	}
	saved, err := f.Edit(t.Context(), file.Path, file.Revision, []byte(strings.ReplaceAll(source, "Hello", "Proposed")))
	requireCode(t, err, "publication_failed")
	if saved.History != nil || saved.Revision != "" {
		t.Fatal("failed rename claimed Saved")
	}
	b, _ := os.ReadFile(filepath.Join(root, file.Path))
	if string(b) != source {
		t.Fatal("failed rename modified source")
	}
	f.historyDir = func() (string, error) { return base, nil }
	view := readView(t, historyClient(t, f), file.Path)
	if len(view.History.Versions) != 1 || view.History.Versions[0].Revision != file.Revision {
		t.Fatal("failed rename fabricated a proposed saved version")
	}
}

func TestHistorySourceChangeDuringRetentionStillRefusesEdit(t *testing.T) {
	_, root, source := canonicalFiles(t)
	base, _ := historyPaths(t)
	f := historyFiles(t, root, base)
	file, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	outside := strings.ReplaceAll(source, "Hello", "Outside change")
	f.historyDir = func() (string, error) {
		if err := os.WriteFile(filepath.Join(root, file.Path), []byte(outside), 0600); err != nil {
			t.Fatal(err)
		}
		return base, nil
	}
	saved, err := f.Edit(t.Context(), file.Path, file.Revision, []byte(strings.ReplaceAll(source, "Hello", "Proposed")))
	requireCode(t, err, "stale_revision")
	if saved.History != nil || saved.Revision != "" {
		t.Fatal("stale source publication claimed Saved")
	}
	b, _ := os.ReadFile(filepath.Join(root, file.Path))
	if string(b) != outside {
		t.Fatal("retention IO bypassed final source CAS")
	}
	f.historyDir = func() (string, error) { return base, nil }
	view := readView(t, historyClient(t, f), file.Path)
	if len(view.History.Versions) != 1 || view.History.Versions[0].Revision != file.Revision {
		t.Fatal("failed edit fabricated a proposed saved version")
	}
}
