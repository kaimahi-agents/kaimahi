//go:build linux || darwin

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historyPaths(t *testing.T) (string, string) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(state, "local-preview", "versions"), root
}

func historyStoreForTest(t *testing.T, base, root string, create bool) *historyStore {
	t.Helper()
	s, err := openHistory(base, root, create)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func TestHistorySnapshotsDedupAndEviction(t *testing.T) {
	base, root := historyPaths(t)
	if _, err := openHistory(base, root, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read = %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("read created history")
	}
	s := historyStoreForTest(t, base, root, true)
	s.limits.perFile, s.limits.records = 2, 3
	first := []byte("# First\r\nMāori 👋\r\n")
	for _, source := range [][]byte{first, first, []byte("B"), first} {
		if err := s.retain(context.Background(), "a.agent.md", source); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.list("a.agent.md")
	if err != nil || len(rows) != 2 || rows[0].Revision != revision(first) || rows[0].VersionID == rows[1].VersionID || rows[0].VersionID < rows[1].VersionID {
		t.Fatalf("per-file eviction/ordering = %+v / %v", rows, err)
	}
	r, err := s.get("a.agent.md", rows[0].VersionID)
	if err != nil || string(r.Source) != string(first) || r.CapturedAt.IsZero() {
		t.Fatalf("exact source lost: %+v / %v", r, err)
	}
	if _, err := s.get("b.agent.md", rows[0].VersionID); err == nil {
		t.Fatal("another file's version readable")
	}
	for _, p := range []string{"b.agent.md", "c.agent.md"} {
		if err := s.retain(context.Background(), p, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.list("a.agent.md")
	if err != nil || len(rows) != 1 || rows[0].Revision != revision(first) {
		t.Fatalf("root eviction lost newest: %+v / %v", rows, err)
	}
	// A budget that holds just one record must evict, not reject growth.
	s.limits.bytes = 800
	if err := s.retain(context.Background(), "a.agent.md", []byte(strings.Repeat("x", 300))); err != nil {
		t.Fatal(err)
	}
	rows, err = s.list("a.agent.md")
	if err != nil || len(rows) != 1 || rows[0].Revision != revision([]byte(strings.Repeat("x", 300))) {
		t.Fatalf("byte eviction = %+v / %v", rows, err)
	}
	s.close()
	reopened := historyStoreForTest(t, base, root, false)
	rows, err = reopened.list("a.agent.md")
	if err != nil || len(rows) != 1 {
		t.Fatalf("restart lost snapshots: %+v / %v", rows, err)
	}
}

func TestHistoryRejectsUnrepresentableInputWithoutPoisoningStore(t *testing.T) {
	base, root := historyPaths(t)
	s := historyStoreForTest(t, base, root, true)
	if err := s.retain(t.Context(), "a.agent.md", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := s.retain(t.Context(), "b.agent.md", []byte{0xff}); err == nil {
		t.Fatal("writer accepted source that the resource cannot represent")
	}
	if rows, err := s.list("a.agent.md"); err != nil || len(rows) != 1 {
		t.Fatal("failed retention poisoned another agent's history", err)
	}
}

func TestHistoryRootIsolationLockAndClear(t *testing.T) {
	base, root := historyPaths(t)
	a := historyStoreForTest(t, base, root, true)
	if err := a.retain(t.Context(), "a.agent.md", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if other, err := openHistory(base, root, true); err == nil {
		other.close()
		t.Fatal("second writer acquired held history lock")
	}
	b := historyStoreForTest(t, base, root+"-other", true)
	if err := b.retain(t.Context(), "a.agent.md", []byte("B")); err != nil {
		t.Fatal("another root was blocked", err)
	}
	if err := a.clear(); err != nil {
		t.Fatal(err)
	}
	if rows, err := a.list("a.agent.md"); err != nil || len(rows) != 0 {
		t.Fatalf("clear failed: %+v / %v", rows, err)
	}
	if rows, err := b.list("a.agent.md"); err != nil || len(rows) != 1 {
		t.Fatal("clear touched another root", err)
	}
	// Replacing the persistent lock pathname invalidates a held lease.
	if err := a.root.Remove("lock"); err != nil {
		t.Fatal(err)
	}
	if err := a.retain(t.Context(), "a.agent.md", []byte("C")); err == nil {
		t.Fatal("replaced lock still authorized writes")
	}
}

func TestHistoryRejectsUnsafeRecordsAndClearRepairsCorruption(t *testing.T) {
	for _, mode := range []string{"corrupt", "hash", "invalid-utf8", "oversize", "symlink", "hardlink", "permissions", "unexpected"} {
		t.Run(mode, func(t *testing.T) {
			base, root := historyPaths(t)
			s := historyStoreForTest(t, base, root, true)
			if err := s.retain(t.Context(), "a.agent.md", []byte("A")); err != nil {
				t.Fatal(err)
			}
			if err := s.retain(t.Context(), "b.agent.md", []byte("B")); err != nil {
				t.Fatal(err)
			}
			rows, _ := s.list("a.agent.md")
			healthy, _ := s.list("b.agent.md")
			p := filepath.Join(s.root.Name(), rows[0].VersionID+".json")
			switch mode {
			case "corrupt":
				if err := os.WriteFile(p, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hash":
				b, _ := os.ReadFile(p)
				if err := os.WriteFile(p, []byte(strings.Replace(string(b), revision([]byte("A")), revision([]byte("B")), 1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				b, _ := os.ReadFile(p)
				var record historyRecord
				if err := json.Unmarshal(b, &record); err != nil {
					t.Fatal(err)
				}
				record.Source = []byte{0xff}
				record.Revision = revision(record.Source)
				b, _ = json.Marshal(record)
				if err := os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(p, maxHistoryRecordBytes+1); err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink":
				outside := filepath.Join(t.TempDir(), "untouched")
				if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if mode == "hardlink" {
					link = os.Link
				}
				if err := link(outside, p); err != nil {
					t.Fatal(err)
				}
				defer func() {
					b, _ := os.ReadFile(outside)
					if string(b) != "outside" {
						t.Error("outside file changed")
					}
				}()
			case "permissions":
				if err := os.Chmod(p, 0644); err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				if err := os.WriteFile(filepath.Join(s.root.Name(), "unowned"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			damagedPayload := mode == "corrupt" || mode == "hash" || mode == "invalid-utf8"
			if _, err := s.get("a.agent.md", rows[0].VersionID); err == nil {
				t.Fatal("damaged or unsafe version readable")
			}
			if damagedPayload {
				if rows, err := s.list("a.agent.md"); err != nil || len(rows) != 0 {
					t.Fatalf("damaged payload was not skipped: %+v / %v", rows, err)
				}
				if rows, err := s.list("b.agent.md"); err != nil || len(rows) != 1 {
					t.Fatalf("damaged payload hid healthy history: %+v / %v", rows, err)
				}
				if r, err := s.get("b.agent.md", healthy[0].VersionID); err != nil || string(r.Source) != "B" {
					t.Fatal("damaged payload blocked healthy read", err)
				}
				if err := s.retain(t.Context(), "a.agent.md", []byte("C")); err != nil {
					t.Fatal("damaged payload blocked new retention", err)
				}
			} else {
				if _, err := s.list("b.agent.md"); err == nil {
					t.Fatal("unsafe object or size violation accepted")
				}
				if _, err := s.get("b.agent.md", healthy[0].VersionID); err == nil {
					t.Fatal("healthy read bypassed unsafe inventory or size bound")
				}
				if err := s.retain(t.Context(), "a.agent.md", []byte("C")); err == nil {
					t.Fatal("unsafe store accepted another record")
				}
			}
			clearErr := s.clear()
			if mode == "corrupt" || mode == "hash" || mode == "invalid-utf8" || mode == "oversize" {
				if clearErr != nil {
					t.Fatal("safe clear could not remove damaged content", clearErr)
				}
			} else if clearErr == nil {
				t.Fatal("clear accepted unsafe filesystem objects")
			}
		})
	}
}

func TestHistoryDamagedPayloadStillConsumesRetentionBudget(t *testing.T) {
	for _, limit := range []string{"records", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			base, root := historyPaths(t)
			s := historyStoreForTest(t, base, root, true)
			if limit == "records" {
				s.limits.records = 2
			} else {
				s.limits.bytes = 800 // Two small records fit, three do not.
			}
			for _, p := range []string{"a.agent.md", "b.agent.md"} {
				if err := s.retain(t.Context(), p, []byte("A")); err != nil {
					t.Fatal(err)
				}
			}
			rows, _ := s.list("a.agent.md")
			p := filepath.Join(s.root.Name(), rows[0].VersionID+".json")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(strings.Repeat("!", len(b))), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.retain(t.Context(), "c.agent.md", []byte("C")); err != nil {
				t.Fatal("damaged payload blocked eviction", err)
			}
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatal("damaged entry was not counted and evicted", err)
			}
			for _, p := range []string{"b.agent.md", "c.agent.md"} {
				if rows, err := s.list(p); err != nil || len(rows) != 1 {
					t.Fatalf("eviction lost healthy %s: %+v / %v", p, rows, err)
				}
			}
		})
	}
}

func TestHistoryTrustedStateAliasButNotOwnedLinks(t *testing.T) {
	base, root := historyPaths(t)
	state := filepath.Dir(filepath.Dir(base))
	alias := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	s := historyStoreForTest(t, filepath.Join(alias, "local-preview", "versions"), root, true)
	if err := s.retain(t.Context(), "a.agent.md", []byte("A")); err != nil {
		t.Fatal(err)
	}
	s.close()
	if err := os.Rename(base, base+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base+"-real", base); err != nil {
		t.Fatal(err)
	}
	if bad, err := openHistory(base, root, false); err == nil {
		bad.close()
		t.Fatal("owned history symlink accepted")
	}
}
