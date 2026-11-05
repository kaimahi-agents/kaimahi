//go:build linux

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

func heldSourceHooks(b *sourcePairBinding) sourcePairPublishHooks {
	return sourcePairPublishHooks{check: b.check, checkPublishing: b.checkPublishing, prepareAbsentParent: b.prepareAbsentParent}
}

// Catches skipped roles, stale strict checks after own stages, mode loss and
// fabricated writes on no-op. Prepare must see prospective raw facts, not a pair.
func TestHeldSourcePairPublicationChanges(t *testing.T) {
	for _, roles := range []string{"neither", "agent", "tests", "both"} {
		t.Run(roles, func(t *testing.T) {
			root := sourceRoot(t)
			agent := filepath.Join(root, "a.agent.md")
			req := sourceRequest("edit", agent)
			sourceWrite(t, agent, "old agent")
			sourceWrite(t, req.Tests.Path, "old tests")
			if err := os.Chmod(agent, 0640); err != nil {
				t.Fatal(err)
			}
			b, err := acquireSourcePair(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			changes := sourcePairChanges{agentBytes: []byte("old agent"), testsBytes: []byte("old tests"), testsPresent: true}
			wantAgent, wantTests := "unchanged", "unchanged"
			if roles == "agent" || roles == "both" {
				changes.agentBytes, wantAgent = []byte("new agent"), "written"
			}
			if roles == "tests" || roles == "both" {
				changes.testsBytes, wantTests = []byte("new tests"), "written"
			}
			hooks := heldSourceHooks(b)
			prepared := false
			hooks.prepare = func(ctx context.Context, result sourcePairPublicationResult) error {
				prepared = true
				if result.Agent.State != wantAgent || result.Tests.State != wantTests || result.Agent.Revision != revision(changes.agentBytes) || result.Tests.Revision != revision(changes.testsBytes) {
					t.Fatalf("prospective facts = %+v", result)
				}
				return nil
			}
			result, err := publishHeldSourcePair(t.Context(), b, changes, hooks)
			if err != nil || !prepared || result.Agent.State != wantAgent || result.Tests.State != wantTests {
				t.Fatalf("result = %+v, %v; prepared = %v", result, err, prepared)
			}
			for path, want := range map[string][]byte{agent: changes.agentBytes, req.Tests.Path: changes.testsBytes} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(want) {
					t.Fatalf("%s = %q, %v", path, got, err)
				}
			}
			info, err := os.Stat(agent)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("replacement mode = %v, %v", info, err)
			}
			if err := b.check(t.Context()); err != nil {
				t.Fatalf("final observations not refreshed: %v", err)
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 2 {
				t.Fatal("stage residue", entries)
			}
		})
	}
}

// Catches checks moved across Prepare, stage/publication boundaries or the
// parent batch; also proves incoming bytes are owned before trusted callbacks.
func TestHeldSourcePairPublicationOrdering(t *testing.T) {
	root := sourceRoot(t)
	req := sourceRequest("create", filepath.Join(root, "b", "a.agent.md"))
	b, err := acquireSourcePair(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	changes := sourcePairChanges{agentBytes: []byte("agent"), testsBytes: []byte("tests"), testsPresent: true}
	hooks := heldSourceHooks(b)
	var events []string
	hooks.check = func(ctx context.Context) error { events = append(events, "check"); return b.check(ctx) }
	hooks.prepare = func(context.Context, sourcePairPublicationResult) error {
		events = append(events, "prepare")
		changes.agentBytes[0], changes.testsBytes[0] = 'X', 'X'
		return nil
	}
	hooks.prepareAbsentParent = func(a *absentSource, created map[string]os.FileInfo) error {
		events = append(events, "parent")
		return b.prepareAbsentParent(a, created)
	}
	hooks.checkPublishing = func(ctx context.Context) error {
		events = append(events, "publishing")
		return b.checkPublishing(ctx)
	}
	hooks.beforePublication = func(context.Context) {
		events = append(events, "history")
		entries, _ := os.ReadDir(filepath.Dir(req.AgentPath))
		if len(entries) != 2 || !strings.HasPrefix(entries[0].Name(), ".kmx-mcp-") || !strings.HasPrefix(entries[1].Name(), ".kmx-mcp-") {
			t.Fatal("history not between staging and first publication", entries)
		}
	}
	result, err := publishHeldSourcePair(t.Context(), b, changes, hooks)
	want := []string{"check", "prepare", "check", "parent", "parent", "publishing", "publishing", "history", "publishing", "publishing", "publishing"}
	if err != nil || !slices.Equal(events, want) || result.Agent.State != "written" || result.Tests.State != "written" {
		t.Fatalf("events = %v, result = %+v, error = %v", events, result, err)
	}
	for path, want := range map[string]string{req.AgentPath: "agent", req.Tests.Path: "tests"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("callback changed owned bytes: %q, %v", got, err)
		}
	}
}

func TestHeldSourcePairPublicationPrepareRefusal(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "veto", true: "change-after-check"}[mutate], func(t *testing.T) {
			root := sourceRoot(t)
			req := sourceRequest("create", filepath.Join(root, "missing", "a.agent.md"))
			b, err := acquireSourcePair(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			hooks := heldSourceHooks(b)
			veto := errors.New("preflight veto")
			hooks.prepare = func(context.Context, sourcePairPublicationResult) error {
				if mutate {
					sourceWrite(t, filepath.Join(root, "foreign"), "foreign")
					return nil
				}
				return veto
			}
			result, err := publishHeldSourcePair(t.Context(), b, sourcePairChanges{agentBytes: []byte("agent"), testsBytes: []byte("tests"), testsPresent: true}, hooks)
			if mutate {
				wantRefusal(t, err, "stale_revision")
			} else if !errors.Is(err, veto) {
				t.Fatalf("lost callback error: %v", err)
			}
			if result.Agent != (SourcePairWrite{State: "not_written"}) || result.Tests != (SourcePairWrite{State: "not_written"}) {
				t.Fatal("forged effects", result)
			}
			if _, err := os.Lstat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
				t.Fatal("mutation before second initial check", err)
			}
		})
	}
}

func TestHeldSourcePairPublicationLockOrder(t *testing.T) {
	root := sourceRoot(t)
	req := sourceRequest("edit", filepath.Join(root, "z.agent.md"))
	req.Tests.Path, req.Tests.Explicit = filepath.Join(root, "a.md"), true
	sourceWrite(t, req.AgentPath, "agent")
	sourceWrite(t, req.Tests.Path, "tests")
	b, err := acquireSourcePair(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	other, err := os.Open(req.AgentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unlock, err := lockTarget(other)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Lexically first tests lock must fail before the busy agent lock.
	b.tests.file.Close()
	_, err = publishHeldSourcePair(t.Context(), b, sourcePairChanges{agentBytes: []byte("agent"), testsBytes: []byte("tests"), testsPresent: true}, heldSourceHooks(b))
	wantRefusal(t, err, "lock_unavailable")
}

func TestHeldSourcePairPublicationConcurrentWinner(t *testing.T) {
	root := sourceRoot(t)
	req := sourceRequest("edit", filepath.Join(root, "a.agent.md"))
	sourceWrite(t, req.AgentPath, "old agent")
	one, err := acquireSourcePair(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer one.close()
	two, err := acquireSourcePair(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer two.close()
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	hooks := heldSourceHooks(one)
	hooks.prepare = func(context.Context, sourcePairPublicationResult) error { close(locked); <-release; return nil }
	changes := sourcePairChanges{agentBytes: []byte("winner")}
	go func() { _, err := publishHeldSourcePair(t.Context(), one, changes, hooks); done <- err }()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("publication skipped locked Prepare: %v", err)
	}
	_, err = publishHeldSourcePair(t.Context(), two, changes, heldSourceHooks(two))
	close(release)
	wantRefusal(t, err, "busy")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, err = publishHeldSourcePair(t.Context(), two, changes, heldSourceHooks(two))
	wantRefusal(t, err, "stale_revision")
	got, _ := os.ReadFile(req.AgentPath)
	if string(got) != "winner" {
		t.Fatalf("winner lost: %q", got)
	}
}

func TestHeldSourcePairPublicationPartialFacts(t *testing.T) {
	for _, window := range []string{"first-error", "second-error", "cancel-after-first", "equal-byte-replacement"} {
		t.Run(window, func(t *testing.T) {
			root := sourceRoot(t)
			req := sourceRequest("create", filepath.Join(root, "a.agent.md"))
			b, err := acquireSourcePair(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prior := renameNoReplace
			t.Cleanup(func() { renameNoReplace = prior })
			attempts := 0
			renameNoReplace = func(fd int, from, to string) error {
				attempts++
				if window == "first-error" || window == "second-error" && attempts == 2 {
					return unix.EIO
				}
				err := prior(fd, from, to)
				if attempts == 1 {
					if window == "cancel-after-first" {
						cancel()
					}
					if window == "equal-byte-replacement" {
						sourceWrite(t, filepath.Join(root, "foreign"), "agent")
						if err := os.Rename(filepath.Join(root, "foreign"), req.AgentPath); err != nil {
							t.Fatal(err)
						}
					}
				}
				return err
			}
			result, err := publishHeldSourcePair(ctx, b, sourcePairChanges{agentBytes: []byte("agent"), testsBytes: []byte("tests"), testsPresent: true}, heldSourceHooks(b))
			if err == nil {
				t.Fatal("missing failure", result)
			}
			wantAgent, wantTests, wantAttempts := "written", "not_written", 1
			if window == "first-error" || window == "equal-byte-replacement" {
				wantAgent = "unknown"
			}
			if window == "second-error" {
				wantTests, wantAttempts = "unknown", 2
			}
			if result.Agent.State != wantAgent || result.Tests.State != wantTests || attempts != wantAttempts || result.Tests.Revision != "" {
				t.Fatalf("facts = %+v, attempts = %d, error = %v", result, attempts, err)
			}
			if wantAgent == "written" && result.Agent.Revision != revision([]byte("agent")) || wantAgent == "unknown" && result.Agent.Revision != "" {
				t.Fatal("incorrect agent revision", result)
			}
			if b.agent != nil && window == "equal-byte-replacement" {
				t.Fatal("adopted foreign equal bytes")
			}
			entries, _ := os.ReadDir(root)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".kmx-mcp-") {
					t.Fatal("leaked stage", entry.Name())
				}
			}
		})
	}
}

func TestPairPublicationRootedAliasesAndCloneSlots(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	sourceWrite(t, filepath.Join(root, "a.agent.md"), "Prompt\n")
	sourceWrite(t, filepath.Join(root, "checks.md"), historyTests)
	if err := os.Symlink("a.agent.md", filepath.Join(root, "alias.agent.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("checks.md", filepath.Join(root, "selected.md")); err != nil {
		t.Fatal(err)
	}
	b, err := f.bindPair(t.Context(), filepath.Join(root, "alias.agent.md"), filepath.Join(root, "selected.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	agent, tests := b.agent, b.tests
	oldTime := agent.parentInfo.ModTime()
	changedTime := oldTime.Add(time.Second)
	if err := os.Chtimes(root, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if err := b.checkPublishing(t.Context()); err != nil {
		t.Fatal("rooted aliases refused", err)
	}
	if b.agent != agent || b.tests != tests || !b.agent.parentInfo.ModTime().Equal(oldTime) {
		t.Fatal("clone checks rebound or refreshed original slots")
	}
	wantRefusal(t, b.check(t.Context()), "stale_revision")
	for _, alias := range []string{"alias.agent.md", "selected.md"} {
		t.Run(alias, func(t *testing.T) {
			path := filepath.Join(root, alias)
			target, _ := os.Readlink(path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other.md", path); err != nil {
				t.Fatal(err)
			}
			wantRefusal(t, b.checkPublishing(t.Context()), "stale_revision")
			os.Remove(path)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPairPublicationPostPublishIdentityReplacement(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	cs := historyClient(t, f)
	prior := renameNoReplace
	defer func() { renameNoReplace = prior }()
	var swapped bool
	renameNoReplace = func(fd int, from, to string) error {
		err := prior(fd, from, to)
		if err != nil {
			return err
		}
		if to == "a.agent.md" {
			target := filepath.Join(root, to)
			old, e := os.Stat(target)
			if e != nil {
				t.Fatal(e)
			}
			replacement := filepath.Join(root, "replacement")
			if e = os.WriteFile(replacement, []byte("Prompt\n"), 0600); e != nil {
				t.Fatal(e)
			}
			if e = os.Rename(replacement, target); e != nil {
				t.Fatal(e)
			}
			now, e := os.Stat(target)
			if e != nil {
				t.Fatal(e)
			}
			swapped = !os.SameFile(old, now)
		}
		return nil
	}
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 3, "path": "a.agent.md", "content": "Prompt\n", "testsContent": historyTests}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var ack pairSaveResult
	json.Unmarshal(raw, &ack)
	a, _ := os.ReadFile(filepath.Join(root, "a.agent.md"))
	b, _ := os.ReadFile(filepath.Join(root, "a.agent.test.md"))
	t.Logf("swappedInode=%v agent=%q testsExact=%v ACK=%s", swapped, a, string(b) == historyTests, raw)
	if !swapped {
		t.Fatal("barrier failed")
	}
	if ack.State == "complete" {
		t.Error("complete ACK adopted post-publication equal-byte replacement")
	}
}

func TestPairPublicationExclusiveFallbackReceipt(t *testing.T) {
	for _, fallback := range []string{"link", "copy"} {
		t.Run(fallback, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			cs := historyClient(t, f)
			if fallback == "copy" {
				forcePublicationFallback(t, unix.ENOSYS, unix.EPERM)
			} else {
				forcePublicationFallback(t, unix.ENOSYS, nil)
			}
			ack := pairSaveCall(t, cs, "create", map[string]any{"path": "a.agent.md", "content": "Prompt\n", "testsContent": historyTests})
			if ack.Agent.Status != "saved" || ack.Tests.Status != "saved" {
				t.Fatal("fallback lost receipt", ack)
			}
			if pairContextRead(t, cs, "a.agent.md", "") != ack.SourceVersion {
				t.Fatal("fallback adopted wrong context")
			}
			a, _ := os.ReadFile(filepath.Join(root, "a.agent.md"))
			b, _ := os.ReadFile(filepath.Join(root, "a.agent.test.md"))
			entries, _ := os.ReadDir(root)
			if string(a) != "Prompt\n" || string(b) != historyTests || len(entries) != 2 {
				t.Fatal("fallback content/residue incorrect")
			}
		})
	}
}

func TestPairPublicationAcknowledgesPartialCreate(t *testing.T) {
	for _, window := range []string{"first-error", "second-error", "cancel-after-first"} {
		t.Run(window, func(t *testing.T) {
			base, root := historyPaths(t)
			f := historyFiles(t, root, base)
			cs := historyClient(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prior := renameNoReplace
			t.Cleanup(func() { renameNoReplace = prior })
			attempts := 0
			renameNoReplace = func(fd int, from, to string) error {
				attempts++
				if window == "first-error" || window == "second-error" && attempts == 2 {
					return unix.EIO
				}
				err := prior(fd, from, to)
				if window == "cancel-after-first" && attempts == 1 {
					cancel()
				}
				return err
			}
			// Call the same operation handler without SDK cancellation discarding the ACK.
			op := Operations(f, nil, nil)["create"].V3
			args, _ := json.Marshal(map[string]any{"path": "a.agent.md", "content": "Prompt\n", "testsContent": historyTests})
			result, err := op.Handler(ctx, &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{Arguments: args}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var ack pairSaveResult
			if json.Unmarshal(data, &ack) != nil || !result.IsError || ack.SnapshotRevision != "" || ack.SourceVersion != "" {
				t.Fatalf("forged complete acknowledgement: %s", data)
			}
			if window == "first-error" {
				if ack.Agent.Status != "uncertain" || ack.Tests.Status != "not_saved" || attempts != 1 {
					t.Fatalf("first effect: %s", data)
				}
			} else {
				if ack.Agent.Status != "saved" {
					t.Fatalf("lost first save: %s", data)
				}
				actual, err := os.ReadFile(filepath.Join(root, "a.agent.md"))
				if err != nil || string(actual) != "Prompt\n" {
					t.Fatal("first saved bytes lost", err)
				}
				if window == "cancel-after-first" && (ack.State != "partial" || ack.Tests.Status != "not_saved" || attempts != 1) {
					t.Fatalf("cancel acknowledgement: %s", data)
				}
				if window == "second-error" && (ack.State != "uncertain" || ack.Tests.Status != "uncertain" || attempts != 2) {
					t.Fatalf("second effect: %s", data)
				}
			}
			if _, err := os.Lstat(filepath.Join(root, "a.agent.test.md")); !os.IsNotExist(err) {
				t.Fatal("unexpected tests publication", err)
			}
			_ = cs
		})
	}
}

func TestPairPublicationForeignAbsentParentHasNoSaves(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	b, err := f.createPairBinding(t.Context(), filepath.Join(root, "new", "a.agent.md"), []byte("Prompt\n"), []byte(historyTests))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err := os.Mkdir(filepath.Join(root, "new"), 0755); err != nil {
		t.Fatal(err)
	}
	ack := b.publish(t.Context(), b.pair, []byte("Prompt\n"), []byte(historyTests))
	if ack.State != "not_saved" || ack.Agent.Status != "not_saved" || ack.Tests.Status != "not_saved" {
		t.Fatalf("false unchanged effects: %+v", ack)
	}
	if _, err := os.Lstat(filepath.Join(root, "new", "a.agent.md")); !os.IsNotExist(err) {
		t.Fatal("adopted foreign directory")
	}
}
