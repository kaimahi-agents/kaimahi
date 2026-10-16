package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func passStoreFixture(t *testing.T) (*suitePassStore, *workspace.NativeSuite, []byte) {
	t.Helper()
	root, prepared, suite, placement, observations := passingFixture(t)
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openSuitePassStore(t.Context(), root, filepath.Join(os.Getenv("KMX_HOME"), "suites"), suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, suite, raw
}

func commitStoreReceipt(t *testing.T, store *suitePassStore, suite *workspace.NativeSuite, raw []byte) string {
	t.Helper()
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := store.commit(t.Context(), raw, suite)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestSuitePassStoreCompletedLatestAndIndependentPartitions(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if _, _, err := store.latest(t.Context(), suite); err == nil {
		t.Fatal("missing proof accepted")
	}
	// Uncommitted names are not proof and do not consume committed capacity.
	writeFile(t, filepath.Join(store.partition, ".native-receipt-leftover"), "uncommitted")
	writeFile(t, filepath.Join(store.partition, "pending.json"), "uncommitted")
	if _, _, err := store.latest(t.Context(), suite); err == nil {
		t.Fatal("uncommitted output eligible")
	}
	first := commitStoreReceipt(t, store, suite, raw)
	receipt, file, err := store.latest(t.Context(), suite)
	if err != nil || file != first || receipt.suiteDigest() != suite.Digest {
		t.Fatalf("lookup: %q %v", file, err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private record: %v", err)
	}
	for _, p := range []string{store.path, store.partition} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("public directory", err)
		}
	}
	second := commitStoreReceipt(t, store, suite, raw)
	_, file, err = store.latest(t.Context(), suite)
	if err != nil || file != second || first == second {
		t.Fatalf("latest proof: %s %v", file, err)
	}
	other := *suite
	other.Digest = "sha256:" + strings.Repeat("a", 64)
	if _, _, err := store.latest(t.Context(), &other); err == nil {
		t.Fatal("other S accepted")
	}
	// Another digest's corrupt records must not affect this partition.
	otherPath := filepath.Join(store.path, strings.Repeat("b", 64))
	if err := os.Mkdir(otherPath, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(otherPath, "00000000000000000001.json"), "corrupt")
	if _, _, err := store.latest(t.Context(), suite); err != nil {
		t.Fatal(err)
	}
	// Corrupt latest committed proof cannot silently select an older pass.
	if err := os.WriteFile(second, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.latest(t.Context(), suite); err == nil {
		t.Fatal("corrupt latest fell back")
	}
}

func TestSuitePassStoreCapacityPreservesExistingProof(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	first := commitStoreReceipt(t, store, suite, raw)
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var record suitePassRecord
	if err := agentsuite.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= maxSuitePassRecords; i++ {
		record.Sequence = uint64(i)
		b, err := agentsuite.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.partition, fmt.Sprintf("%020d.json", i)), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.reserve(t.Context()); err == nil {
		t.Fatal("new registration exceeded capacity")
	}
	if _, _, err := store.latest(t.Context(), suite); err != nil {
		t.Fatalf("capacity blocked existing proof: %v", err)
	}
}

func TestSuitePassStoreStrictCommittedRecords(t *testing.T) {
	for _, change := range []string{"unknown", "duplicate", "sequence", "suite", "workspace", "receipt", "oversize", "symlink", "public"} {
		t.Run(change, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			path := commitStoreReceipt(t, store, suite, raw)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "unknown":
				data = append([]byte(`{"unknown":1,`), data[1:]...)
			case "duplicate":
				data = append([]byte(`{"schemaVersion":1,`), data[1:]...)
			case "sequence":
				data = []byte(strings.Replace(string(data), `"sequence":1`, `"sequence":2`, 1))
			case "suite":
				data = []byte(strings.ReplaceAll(string(data), suite.Digest, "sha256:"+strings.Repeat("0", 64)))
			case "workspace":
				data = []byte(strings.Replace(string(data), store.identity, strings.Repeat("0", 64), 1))
			case "receipt":
				data = []byte(strings.Replace(string(data), `"result":"pass"`, `"result":"fail"`, 1))
			case "oversize":
				data = []byte(strings.Repeat("x", maxSuitePassRecordBytes+1))
			case "symlink":
				target := filepath.Join(privateSuiteDir(t), "record")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if change != "symlink" && change != "public" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := store.latest(t.Context(), suite); err == nil {
				t.Fatal("invalid committed state accepted")
			}
		})
	}
}

func TestSuitePassStoreRefusesUnsafeState(t *testing.T) {
	for _, change := range []string{"workspace", "symlink home", "public store", "symlink lock", "public lock", "moved parent", "canceled"} {
		t.Run(change, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			state := os.Getenv("KMX_HOME")
			root, cache, path := store.workspace, store.cache, store.path
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "workspace":
				t.Setenv("KMX_HOME", root)
			case "symlink home":
				alias := filepath.Join(privateSuiteDir(t), "home")
				if err := os.Symlink(state, alias); err != nil {
					t.Fatal(err)
				}
				t.Setenv("KMX_HOME", alias)
			case "public store":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink lock":
				if err := os.Remove(filepath.Join(path, "lock")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(privateSuiteDir(t), "other"), filepath.Join(path, "lock")); err != nil {
					t.Fatal(err)
				}
			case "public lock":
				if err := os.Chmod(filepath.Join(path, "lock"), 0644); err != nil {
					t.Fatal(err)
				}
			case "moved parent":
				opened, err := openSuitePassStore(t.Context(), root, cache, suite.Digest)
				if err != nil {
					t.Fatal(err)
				}
				defer opened.Close()
				if err := opened.reserve(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-moved"); err != nil {
					t.Fatal(err)
				}
				if _, err := opened.commit(t.Context(), raw, suite); err == nil {
					t.Fatal("moved directory accepted")
				}
				return
			}
			ctx := t.Context()
			if change == "canceled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			opened, err := openSuitePassStore(ctx, root, cache, suite.Digest)
			if err == nil {
				opened.Close()
				t.Fatal("unsafe store accepted")
			}
		})
	}
}

func TestSuitePassStoreCompletionSyncFailureKeepsVisibleProof(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.completion.directory.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := store.commit(t.Context(), raw, suite)
	if err == nil || !strings.Contains(err.Error(), "persistence uncertain") || !strings.Contains(err.Error(), record) {
		t.Fatalf("missing postcommit disclosure: %v", err)
	}
	if _, file, err := store.latest(t.Context(), suite); err != nil || file != record {
		t.Fatalf("visible proof revoked: %s %v", file, err)
	}
}

func TestSuiteWorkspaceExclusionProcessExitAndStableInode(t *testing.T) {
	if root := os.Getenv("KMX_PASS_LOCK_CHILD"); root != "" {
		store, err := openSuitePassStore(context.Background(), root, filepath.Join(os.Getenv("KMX_HOME"), "suites"), "sha256:"+strings.Repeat("a", 64))
		if err == nil {
			_ = store
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(19)
	}
	store, suite, _ := passStoreFixture(t)
	lock := filepath.Join(store.path, "lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	if competing, err := openSuitePassStore(t.Context(), store.workspace, store.cache, suite.Digest); err == nil {
		competing.Close()
		t.Fatal("same-process lock not exclusive")
	}
	run := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSuiteWorkspaceExclusionProcessExitAndStableInode$")
		cmd.Env = append(os.Environ(), "KMX_PASS_LOCK_CHILD="+store.workspace)
		return cmd.Run()
	}
	var exit *exec.ExitError
	if err := run(); !errors.As(err, &exit) || exit.ExitCode() != 19 {
		t.Fatalf("competing process acquired lock: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("closed lock retained authority: %v", err)
	}
	if err := run(); err != nil {
		t.Fatalf("process exit left blockade: %v", err)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode replaced: %v", err)
	}
}
