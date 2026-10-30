package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxHistoryRecordBytes = 6*maxFileBytes + 4096

var historyName = regexp.MustCompile(`^[0-9]{20}-[a-f0-9]{32}\.json$`)
var historyKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

type historyLimits struct {
	perFile, records int
	bytes            int64
}

// AgentVersion lists source snapshots, not previous execution results.
type AgentVersion struct {
	VersionID        string    `json:"versionId"`
	Revision         string    `json:"revision"`
	CapturedAt       time.Time `json:"capturedAt"`
	SourceBytes      int       `json:"sourceBytes"`
	URI              string    `json:"uri"`
	SourceKind       string    `json:"sourceKind"`
	Label            string    `json:"label"`
	SnapshotRevision string    `json:"snapshotRevision,omitempty"`
	TestsState       string    `json:"testsState,omitempty"`
	TestsRevision    string    `json:"testsRevision,omitempty"`
	SelectionKey     string    `json:"selectionKey,omitempty"`
}

type historyRecord struct {
	SchemaVersion int          `json:"schemaVersion"`
	VersionID     string       `json:"versionId"`
	FileKey       string       `json:"fileKey"`
	Revision      string       `json:"revision"`
	CapturedAt    time.Time    `json:"capturedAt"`
	Source        []byte       `json:"source"`
	Pair          *historyPair `json:"pair,omitempty"`
}

type historyEntry struct {
	name   string
	info   os.FileInfo
	record historyRecord
}

type historyAnchor struct {
	parent *os.Root
	name   string
	info   os.FileInfo
}

type historyStore struct {
	root        *os.Root
	roots       []*os.Root
	anchors     []historyAnchor
	statePath   string
	stateInfo   os.FileInfo
	lock        *os.File
	lockInfo    os.FileInfo
	unlock      func()
	releaseGate func()
	limits      historyLimits
}

func historyHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func (s *historyStore) close() {
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
	for i := len(s.roots) - 1; i >= 0; i-- {
		_ = s.roots[i].Close()
	}
	s.roots = nil
	if s.releaseGate != nil {
		s.releaseGate()
		s.releaseGate = nil
	}
}

func (s *historyStore) unchanged() bool {
	state, err := os.Stat(s.statePath)
	if err != nil || !os.SameFile(state, s.stateInfo) {
		return false
	}
	for _, a := range s.anchors {
		current, err := a.parent.Lstat(a.name)
		if err != nil || !os.SameFile(current, a.info) || current.Mode() != a.info.Mode() || !historyOwned(current, true) {
			return false
		}
	}
	current, err := s.root.Lstat("lock")
	return err == nil && os.SameFile(current, s.lockInfo) && historyOwned(current, false) && current.Size() == 0
}

func (s *historyStore) scan(bounded bool) ([]historyEntry, error) {
	if !s.unchanged() {
		return nil, refusal("history_unavailable")
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, refusal("history_unavailable")
	}
	// One pending file and one crash-leftover record are the only allowances.
	names, readErr := dir.ReadDir(203)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(names) > 202 {
		return nil, refusal("history_unavailable")
	}
	entries := []historyEntry{}
	var total int64
	for _, d := range names {
		name := d.Name()
		if name == "lock" {
			continue
		}
		if name != ".pending" && !historyName.MatchString(name) {
			return nil, refusal("history_unavailable")
		}
		info, err := s.root.Lstat(name)
		if err != nil || !historyOwned(info, false) {
			return nil, refusal("history_unavailable")
		}
		e := historyEntry{name: name, info: info}
		if bounded {
			total += info.Size()
			if info.Size() > maxHistoryRecordBytes || total > (32<<20)+maxHistoryRecordBytes {
				return nil, refusal("history_unavailable")
			}
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b historyEntry) int { return strings.Compare(a.name, b.name) })
	return entries, nil
}

func (s *historyStore) load() ([]historyEntry, error) {
	entries, err := s.scan(true)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].name != ".pending" {
			if _, err := s.readRecord(&entries[i]); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}

// Damaged payloads leave an empty record, but their inventory entry and bytes
// still consume root capacity. Filesystem and identity failures remain errors.
func (s *historyStore) readRecord(e *historyEntry) (bool, error) {
	f, err := openTarget(s.root, e.name)
	if err != nil {
		return false, refusal("history_unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(info, e.info) || !historyOwned(info, false) {
		return false, refusal("history_unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxHistoryRecordBytes+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(b) > maxHistoryRecordBytes || int64(len(b)) != e.info.Size() || !sameHistoryEntry(after, e.info) {
		return false, refusal("history_unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	e.record = historyRecord{}
	var r historyRecord
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || !r.validSourceKind() || r.VersionID+".json" != e.name || !historyKey.MatchString(r.FileKey) || r.CapturedAt.IsZero() || len(r.Source) == 0 || len(r.Source) > maxFileBytes || !utf8.Valid(r.Source) || r.Revision != revision(r.Source) {
		return false, nil
	}
	e.record = r
	return true, nil
}

func sameHistoryEntry(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && historyOwned(a, false)
}

func (s *historyStore) remove(e historyEntry) error {
	info, err := s.root.Lstat(e.name)
	if err != nil || !s.unchanged() || !sameHistoryEntry(info, e.info) || s.root.Remove(e.name) != nil {
		return refusal("history_unavailable")
	}
	return nil
}

func (s *historyStore) sync() error {
	d, err := s.root.Open(".")
	if err != nil {
		return refusal("history_unavailable")
	}
	err = errors.Join(d.Sync(), d.Close())
	if err != nil || !s.unchanged() {
		return refusal("history_unavailable")
	}
	return nil
}

func (s *historyStore) retain(ctx context.Context, p string, source []byte) error {
	return s.retainRecord(ctx, p, historyRecord{SchemaVersion: 1, Source: source})
}

func (s *historyStore) retainRecord(ctx context.Context, p string, r historyRecord) error {
	if !agentPath(p) || len(r.Source) == 0 || len(r.Source) > maxFileBytes || !utf8.Valid(r.Source) || !r.validSourceKind() {
		return refusal("history_unavailable")
	}
	entries, err := s.load()
	if err != nil {
		return err
	}
	key := historyHash(p)
	identity := revision(r.Source)
	if r.Pair != nil {
		identity = r.Pair.SnapshotRevision
	}
	var sequence uint64
	count, perFile, total := 0, 0, int64(0)
	newest := ""
	for _, e := range entries {
		if e.name == ".pending" {
			continue
		}
		n, err := strconv.ParseUint(e.name[:20], 10, 64)
		if err != nil || n == ^uint64(0) {
			return refusal("history_unavailable")
		}
		sequence = max(sequence, n)
		count++
		total += e.info.Size()
		if e.record.FileKey == key {
			perFile++
			newest = ""
			if e.record.SchemaVersion == r.SchemaVersion {
				newest = e.record.Revision
				if e.record.Pair != nil {
					newest = e.record.Pair.SnapshotRevision
				}
			}
		}
	}
	if newest == identity {
		return nil
	}
	var random [16]byte
	_, _ = rand.Read(random[:]) // crypto/rand.Read fills the buffer or terminates.
	r.VersionID, r.FileKey = fmt.Sprintf("%020d-%x", sequence+1, random), key
	r.Revision, r.CapturedAt = revision(r.Source), time.Now().UTC()
	b, err := json.Marshal(r)
	if err != nil || len(b) > maxHistoryRecordBytes || int64(len(b)) > s.limits.bytes {
		return refusal("history_unavailable")
	}
	for _, e := range entries {
		if err := cancelled(ctx); err != nil {
			return err
		}
		if e.name == ".pending" {
			if err := s.remove(e); err != nil {
				return err
			}
			continue
		}
		if perFile < s.limits.perFile && count < s.limits.records && total+int64(len(b)) <= s.limits.bytes {
			continue
		}
		// When only the per-file cap is full, preserve other files' history.
		if perFile >= s.limits.perFile && count < s.limits.records && total+int64(len(b)) <= s.limits.bytes && e.record.FileKey != key {
			continue
		}
		if err := s.remove(e); err != nil {
			return err
		}
		count--
		total -= e.info.Size()
		if e.record.FileKey == key {
			perFile--
		}
	}
	if err := cancelled(ctx); err != nil {
		return err
	}
	f, err := historyOpenFile(s.root, ".pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return refusal("history_unavailable")
	}
	defer s.root.Remove(".pending")
	_, writeErr := f.Write(b)
	info, statErr := f.Stat()
	syncErr := f.Sync()
	closeErr := f.Close()
	if errors.Join(writeErr, statErr, syncErr, closeErr) != nil || !historyOwned(info, false) || info.Size() != int64(len(b)) || !s.unchanged() {
		return refusal("history_unavailable")
	}
	if err := cancelled(ctx); err != nil {
		return err
	}
	current, err := s.root.Lstat(".pending")
	if err != nil || !sameHistoryEntry(current, info) || renameExclusive(s.root, ".pending", r.VersionID+".json") != nil {
		return refusal("history_unavailable")
	}
	return s.sync()
}

func (s *historyStore) list(p string) ([]AgentVersion, error) {
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	rows := []AgentVersion{}
	for i := len(entries) - 1; i >= 0; i-- {
		r := entries[i].record
		if entries[i].name != ".pending" && r.FileKey == historyHash(p) {
			rows = append(rows, sourceVersion(r))
		}
	}
	if !s.unchanged() {
		return nil, refusal("history_unavailable")
	}
	return rows, nil
}

func (s *historyStore) get(p, id string) (historyRecord, error) {
	entries, err := s.scan(true)
	if err != nil {
		return historyRecord{}, err
	}
	for _, e := range entries {
		if e.name == id+".json" {
			valid, err := s.readRecord(&e)
			if err != nil {
				return historyRecord{}, err
			}
			if valid && e.record.FileKey == historyHash(p) && s.unchanged() {
				return e.record, nil
			}
			break
		}
	}
	return historyRecord{}, refusal("not_found")
}

func (s *historyStore) clear() error {
	entries, err := s.scan(false)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.remove(e); err != nil {
			return err
		}
	}
	return s.sync()
}
