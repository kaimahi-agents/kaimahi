package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func failureStoreFixture(t *testing.T) (string, suiteFailureDetail) {
	t.Helper()
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	frame, obs := failureDetailFixture("fictional answer")
	d := *captureSuiteFailure(frame, obs, "fictional answer", nil, nil)
	d.OperationID = "op-" + strings.Repeat("a", 32)
	d.SuiteDigest = "sha256:" + strings.Repeat("b", 64)
	d.Runtime, d.MemberID = "orka", "fictional"
	d.Complete, d.Attributed = true, true
	return root, d
}

func TestSuiteFailureStoreReaderBudgets(t *testing.T) {
	_, d := failureStoreFixture(t)
	d.ID = "f-" + strings.Repeat("d", 32)
	d.Assertions = make([]suiteFailureAssertion, 5)
	for i := range d.Assertions {
		d.Assertions[i] = suiteFailureAssertion{ID: fmt.Sprint(i), Type: "contains", Verdict: "fail", Excerpt: strings.Repeat("x", 512), ExcerptKind: "leading"}
	}
	if _, err := marshalSuiteFailureRecord(suiteFailureRecord{1, 1, d}, nil); err == nil {
		t.Fatal("record exceeds displayed excerpt budget")
	}
}

func TestSuiteFailureStoreBounds(t *testing.T) {
	_, d := failureStoreFixture(t)
	d.ID = "f-" + strings.Repeat("d", 32)
	d.Answer = strings.Repeat("<", 64<<10)
	d.Assertions = make([]suiteFailureAssertion, 1000)
	for i := range d.Assertions {
		d.Assertions[i] = suiteFailureAssertion{ID: fmt.Sprintf("%04d", i) + strings.Repeat("x", 252), Type: "contains", Verdict: "fail", Expected: strings.Repeat("<", 512), Reasons: []string{strings.Repeat("<", 512)}}
	}
	record := suiteFailureRecord{SchemaVersion: 1, Sequence: 1, Detail: d}
	raw, err := marshalSuiteFailureRecord(record, nil)
	if err != nil || len(raw) > maxSuiteFailureRecordBytes {
		t.Fatal("serialized byte limit", len(raw), err)
	}
	var got suiteFailureRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Detail.ContentTruncated || got.Detail.OmittedAssertions == 0 || got.Detail.Answer != "" || got.Detail.AnswerStatus != "omitted" {
		t.Fatal("overflow metadata missing")
	}
	if len(d.Answer) != 64<<10 || len(d.Assertions) != 1000 || d.Assertions[0].Expected == "" {
		t.Fatal("store mutated live diagnostics")
	}
	again, err := marshalSuiteFailureRecord(record, nil)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("non-deterministic truncation", err)
	}
}

func TestSuiteFailureStoreInterruptedWrite(t *testing.T) {
	root, d := failureStoreFixture(t)
	for i := 0; i < 10; i++ {
		if _, err := saveSuiteFailure(t.Context(), root, d); err != nil {
			t.Fatal(err)
		}
	}
	s, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(path, ".pending")
	if err := os.WriteFile(pending, []byte(`{"partial`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.scan()
	if err != nil || len(entries) != 10 {
		t.Fatal("staging file hid healthy history", err)
	}
	if _, err := s.save(t.Context(), d); !errors.Is(err, errSuiteFailureStore) {
		t.Fatal("overwrote pending write", err)
	}
	entries, err = s.scan()
	if err != nil || len(entries) != 10 {
		t.Fatal("pending write evicted history", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := clearSuiteFailures(t.Context(), root, entries[0].record.Detail.ID); err != nil || n != 1 {
		t.Fatal("single clear blocked by stage", n, err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatal("single clear touched pending write", err)
	}
	if n, err := clearSuiteFailures(t.Context(), root, ""); err != nil || n != 10 {
		t.Fatal("scoped clear did not recover interrupted write", n, err)
	}
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatal("pending file remains", err)
	}
	if _, err := saveSuiteFailure(t.Context(), root, d); err != nil {
		t.Fatal("retention did not recover", err)
	}
}

func TestSuiteFailureStorePublicationUncertainty(t *testing.T) {
	root, d := failureStoreFixture(t)
	s, err := openSuiteFailureStore(root, true)
	if err != nil {
		t.Fatal(err)
	}
	s.rename = func(from, to string) error {
		if err := s.root.Rename(from, to); err != nil {
			return err
		}
		return s.root.Close()
	}
	id, err := s.save(t.Context(), d)
	_ = s.Close() // The injected failure already closed the root handle.
	if !suiteFailureID.MatchString(id) || !errors.Is(err, errSuiteFailureStore) {
		t.Fatal("visible publication lost its ID", id, err)
	}
	fresh, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	entries, err := fresh.scan()
	if err != nil || len(entries) != 1 || entries[0].record.Detail.ID != id {
		t.Fatal("published record lost", err)
	}
}

func TestSuiteFailureStoreCapacityWriteFailure(t *testing.T) {
	root, d := failureStoreFixture(t)
	var first string
	for i := 0; i < 10; i++ {
		id, err := saveSuiteFailure(t.Context(), root, d)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	s, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.writeFile = func(f *os.File, raw []byte) (int, error) { return f.Write(raw[:1]) }
	if _, err := s.save(t.Context(), d); !errors.Is(err, errSuiteFailureStore) {
		t.Fatal(err)
	}
	rows, err := s.scan()
	if err != nil || len(rows) != 9 {
		t.Fatal("capacity contract changed", err)
	}
	for _, row := range rows {
		if row.record.Detail.ID == first {
			t.Fatal("oldest record was not pre-pruned")
		}
	}
}

func TestSuiteFailureStoreAtomicErrors(t *testing.T) {
	for _, mode := range []string{"short-write", "close", "rename"} {
		t.Run(mode, func(t *testing.T) {
			root, d := failureStoreFixture(t)
			s, err := openSuiteFailureStore(root, true)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			switch mode {
			case "short-write":
				s.writeFile = func(f *os.File, b []byte) (int, error) { return f.Write(b[:1]) }
			case "close":
				s.closeFile = func(f *os.File) error { _ = f.Close(); return io.ErrClosedPipe }
			case "rename":
				s.rename = func(string, string) error { return io.ErrUnexpectedEOF }
			}
			if _, err := s.save(context.Background(), d); err == nil {
				t.Fatal("fault accepted")
			}
			entries, err := s.scan()
			if err != nil || len(entries) != 0 {
				t.Fatal("partial publication or leftover stage", entries, err)
			}
		})
	}
}

func TestSuiteFailureStoreUnsafe(t *testing.T) {
	for _, mode := range []string{"symlink", "fifo", "mode", "hardlink", "oversize", "corrupt", "duplicate", "missing-state", "inconsistent-state", "identity-controls", "invalid-utf8", "pending-mode", "pending-link", "pending-fifo", "unknown", "lock", "directory"} {
		t.Run(mode, func(t *testing.T) {
			root, d := failureStoreFixture(t)
			id, err := saveSuiteFailure(context.Background(), root, d)
			if err != nil {
				t.Fatal(err)
			}
			s, err := openSuiteFailureStore(root, false)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := s.scan()
			if err != nil {
				t.Fatal(err)
			}
			path := s.path
			target := filepath.Join(path, entries[0].name)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink":
				_ = os.Remove(target)
				err = os.Symlink(filepath.Join(t.TempDir(), "outside"), target)
			case "fifo":
				_ = os.Remove(target)
				err = unix.Mkfifo(target, 0600)
			case "mode":
				err = os.Chmod(target, 0644)
			case "hardlink":
				err = os.Link(target, filepath.Join(t.TempDir(), "copy"))
			case "oversize", "corrupt", "inconsistent-state", "invalid-utf8":
				raw, e := os.ReadFile(target)
				if e != nil {
					t.Fatal(e)
				}
				switch mode {
				case "oversize":
					raw = append(raw, bytes.Repeat([]byte(" "), maxSuiteFailureRecordBytes+1-len(raw))...)
				case "corrupt":
					raw = bytes.Replace(raw, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":99`), 1)
				case "inconsistent-state":
					raw = bytes.Replace(raw, []byte(`"complete":true`), []byte(`"complete":false`), 1)
				case "invalid-utf8":
					raw = bytes.Replace(raw, []byte("fictional"), []byte{0xff}, 1)
				}
				err = os.WriteFile(target, raw, 0600)
			case "duplicate", "missing-state":
				raw, e := os.ReadFile(target)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "duplicate" {
					raw = bytes.Replace(raw, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":1,"schemaVersion":1`), 1)
				} else {
					raw = bytes.Replace(raw, []byte(`"complete":true,`), nil, 1)
				}
				err = os.WriteFile(target, raw, 0600)
			case "identity-controls":
				raw, e := os.ReadFile(target)
				if e != nil {
					t.Fatal(e)
				}
				var record suiteFailureRecord
				if json.Unmarshal(raw, &record) != nil {
					t.Fatal("bad fixture")
				}
				record.Detail.CaseID = "first\nsecond"
				raw, e = json.Marshal(record)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(target, raw, 0600)
			case "pending-mode":
				pending := filepath.Join(path, suiteFailurePendingName)
				if err = os.WriteFile(pending, nil, 0600); err == nil {
					err = os.Chmod(pending, 0644)
				}
			case "pending-link":
				err = os.Symlink(target, filepath.Join(path, suiteFailurePendingName))
			case "pending-fifo":
				err = unix.Mkfifo(filepath.Join(path, suiteFailurePendingName), 0600)
			case "unknown":
				err = os.WriteFile(filepath.Join(path, "unrelated"), []byte("keep"), 0600)
			case "lock":
				err = os.Chmod(filepath.Join(path, "lock"), 0644)
			case "directory":
				err = os.Chmod(path, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if n, err := clearSuiteFailures(context.Background(), root, id); !errors.Is(err, errSuiteFailureStore) || n != 0 {
				t.Fatal("unsafe clear accepted", n, err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatal("refusal removed target", err)
			}
		})
	}
}

type suiteFailureForeignInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i suiteFailureForeignInfo) Sys() any { return &i.stat }
func TestSuiteFailureStoreOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	if safeSuiteFailureFile(suiteFailureForeignInfo{info, stat}) {
		t.Fatal("foreign-owner file accepted")
	}
}

func TestSuiteFailureStoreClearAndLock(t *testing.T) {
	root, d := failureStoreFixture(t)
	first, err := saveSuiteFailure(context.Background(), root, d)
	if err != nil {
		t.Fatal(err)
	}
	second, err := saveSuiteFailure(context.Background(), root, d)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := clearSuiteFailures(context.Background(), root, first); !errors.Is(err, errSuiteFailureBusy) || n != 0 {
		t.Fatal("concurrent clear bypassed lock", n, err)
	}
	if _, err := saveSuiteFailure(context.Background(), root, d); !errors.Is(err, errSuiteFailureBusy) {
		t.Fatal("concurrent write bypassed lock", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(os.Getenv("KMX_HOME"), "passing")
	if err := os.WriteFile(unrelated, []byte("not diagnostics"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := clearSuiteFailures(context.Background(), root, first); err != nil || n != 1 {
		t.Fatal("clear one", n, err)
	}
	if n, err := clearSuiteFailures(context.Background(), root, first); !errors.Is(err, errSuiteFailureMissing) || n != 0 {
		t.Fatal("missing id accepted", n, err)
	}
	if n, err := clearSuiteFailures(context.Background(), root, ""); err != nil || n != 1 {
		t.Fatal("clear all", n, err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("clear reached passing state", err)
	}
	_ = second
}

func TestSuiteFailureStorePermissions(t *testing.T) {
	if mode := os.Getenv("KMX_FAILURE_UMASK"); mode != "" {
		mask := 0077
		if mode == "022" {
			mask = 0022
		}
		unix.Umask(mask)
		TestSuiteFailureStoreRetention(t)
		return
	}
	for _, mode := range []string{"077", "022"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSuiteFailureStorePermissions$", "-test.count=1")
		cmd.Env = append(os.Environ(), "KMX_FAILURE_UMASK="+mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask%s: %s %v", mode, out, err)
		}
	}
}

func TestSuiteFailureStoreClippingPrivacy(t *testing.T) {
	for _, limit := range []int{512, 64 << 10} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			root, meta := failureStoreFixture(t)
			answer := strings.Repeat("p", limit-33) + " " + strings.Repeat("a", 32) + "z"
			frame, obs := failureDetailFixture(answer)
			d := *captureSuiteFailure(frame, obs, answer, nil, nil)
			d.OperationID, d.SuiteDigest, d.Runtime, d.MemberID = meta.OperationID, meta.SuiteDigest, meta.Runtime, meta.MemberID
			d.Complete, d.Attributed = true, true
			if limit == 512 && d.Assertions[0].Excerpt != "" {
				t.Fatal("clipping created a credential-shaped excerpt")
			}
			if limit == 64<<10 && d.AnswerStatus != "withheld" {
				t.Fatal("clipping created a credential-shaped answer")
			}
			if _, err := saveSuiteFailure(t.Context(), root, d); err != nil {
				t.Fatal("one clipped field discarded the diagnostic", err)
			}
		})
	}
}

func TestSuiteFailureStorePrivacy(t *testing.T) {
	root, d := failureStoreFixture(t)
	d.Answer = "First-Field: private-one\nSecond-Field: private-two"
	if _, err := saveSuiteFailure(context.Background(), root, d); err == nil {
		t.Fatal("unsafe supplied detail saved")
	}
	frame, obs := failureDetailFixture(d.Answer)
	safe := *captureSuiteFailure(frame, obs, d.Answer, nil, nil)
	safe.OperationID, safe.SuiteDigest, safe.Runtime, safe.MemberID = d.OperationID, d.SuiteDigest, d.Runtime, d.MemberID
	safe.Complete, safe.Attributed = true, true
	if _, err := saveSuiteFailure(context.Background(), root, safe); err != nil {
		t.Fatal("withheld diagnostic lost", err)
	}
	s, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entries, err := s.scan()
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	raw, err := s.root.ReadFile(entries[0].name)
	if err != nil || bytes.Contains(raw, []byte("private-one")) {
		t.Fatal("header content retained", err)
	}
}

func TestSuiteFailureStoreRetention(t *testing.T) {
	root, d := failureStoreFixture(t)
	var first string
	for i := 0; i < 11; i++ {
		if i%2 == 0 {
			d.SuiteDigest = "sha256:" + strings.Repeat("c", 64)
		} else {
			d.SuiteDigest = "sha256:" + strings.Repeat("b", 64)
		}
		id, err := saveSuiteFailure(context.Background(), root, d)
		if err != nil || !suiteFailureID.MatchString(id) {
			t.Fatalf("save index %d: %s %v", i, id, err)
		}
		if i == 0 {
			first = id
		}
	}
	store, err := openSuiteFailureStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatal("retention bound", len(entries))
	}
	digests := map[string]bool{}
	for _, entry := range entries {
		if entry.record.Detail.ID == first {
			t.Fatal("oldest run retained")
		}
		digests[entry.record.Detail.SuiteDigest] = true
		if _, err := readSuitePassingReceipt(context.Background(), filepath.Join(store.path, entry.name)); err == nil {
			t.Fatal("failure record accepted as passing evidence")
		}
		info, err := store.root.Lstat(entry.name)
		if err != nil || info.Mode().Perm() != 0600 || info.Size() > maxSuiteFailureRecordBytes {
			t.Fatal("unsafe retained file", info, err)
		}
	}
	if len(digests) != 2 {
		t.Fatal("revision change reset history")
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("unsafe partition", err)
	}
}
