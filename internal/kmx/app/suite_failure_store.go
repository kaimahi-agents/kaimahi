package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"golang.org/x/sys/unix"
)

const maxSuiteFailureRecords = 10
const suiteFailurePendingName = ".pending"
const maxSuiteFailureRecordBytes = 128 << 10

var (
	errSuiteFailureStore    = errors.New("evaluation failure diagnostics unavailable or unsafe; inspect private KMX state")
	errSuiteFailureMissing  = errors.New("no retained evaluation failure found for this workspace")
	errSuiteFailureBusy     = errors.New("evaluation failure diagnostics busy; retry after the other command exits")
	suiteFailureID          = regexp.MustCompile(`^f-[0-9a-f]{32}$`)
	suiteFailureOperationID = regexp.MustCompile(`^op-[0-9a-f]{32}$`)
	suiteFailureName        = regexp.MustCompile(`^([0-9]{20})-(f-[0-9a-f]{32})\.json$`)
)

// These unsigned local records never grant execution, evidence or publication authority.
type suiteFailureRecord struct {
	SchemaVersion int                `json:"schemaVersion"`
	Sequence      uint64             `json:"sequence"`
	Detail        suiteFailureDetail `json:"detail"`
}
type suiteFailureEntry struct {
	name   string
	info   os.FileInfo
	record suiteFailureRecord
}
type suiteFailureStore struct {
	root      *os.Root
	lock      *os.File
	lockInfo  os.FileInfo
	path      string
	writeFile func(*os.File, []byte) (int, error)
	closeFile func(*os.File) error
	rename    func(string, string) error
}

func suiteFailureOwned(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func safeSuiteFailureDirectory(info os.FileInfo) bool {
	return suiteFailureOwned(info) && info.IsDir() && info.Mode().Perm() == 0700
}
func safeSuiteFailureFile(info os.FileInfo) bool {
	if !suiteFailureOwned(info) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxSuiteFailureRecordBytes {
		return false
	}
	return info.Sys().(*syscall.Stat_t).Nlink == 1
}

func openSuiteFailureStore(workspaceRoot string, create bool) (_ *suiteFailureStore, err error) {
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	info, e := os.Stat(workspaceRoot)
	if e != nil || !info.IsDir() {
		return nil, errSuiteFailureStore
	}
	state, e := config.StateDir()
	if e != nil {
		return nil, errSuiteFailureStore
	}
	state, e = filepath.Abs(state)
	if e != nil {
		return nil, errSuiteFailureStore
	}
	base := filepath.Join(state, "eval-failures")
	path := filepath.Join(base, fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceRoot))))
	if suiteReceiptInside(base, workspaceRoot) {
		return nil, errSuiteFailureStore
	}
	if create {
		for i, p := range []string{state, base, path} {
			if ensureSuitePassDirectory(p, i > 0) != nil {
				return nil, errSuiteFailureStore
			}
		}
	}
	for _, p := range []string{base, path} {
		info, e := os.Lstat(p)
		if os.IsNotExist(e) && !create {
			return nil, errSuiteFailureMissing
		}
		if e != nil || !safeSuiteFailureDirectory(info) {
			return nil, errSuiteFailureStore
		}
	}
	root, _, _, e := openSessionReferenceDirectory(filepath.Join(path, "lock"))
	if e != nil {
		return nil, errSuiteFailureStore
	}
	s := &suiteFailureStore{root: root, path: path, writeFile: (*os.File).Write, closeFile: (*os.File).Close, rename: root.Rename}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	dir, e := root.Open(".")
	if e != nil {
		return nil, errSuiteFailureStore
	}
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT
	}
	fd, e := unix.Openat(int(dir.Fd()), "lock", flags, 0600)
	closeErr := dir.Close()
	if e != nil || closeErr != nil {
		if e == nil {
			_ = unix.Close(fd)
		}
		return nil, errSuiteFailureStore
	}
	s.lock = os.NewFile(uintptr(fd), "lock")
	s.lockInfo, e = s.lock.Stat()
	if e != nil || !safeSuiteFailureFile(s.lockInfo) || s.lockInfo.Size() != 0 {
		return nil, errSuiteFailureStore
	}
	if unix.Flock(int(s.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, errSuiteFailureBusy
	}
	if s.unchanged() != nil {
		return nil, errSuiteFailureStore
	}
	return s, nil
}
func (s *suiteFailureStore) Close() error {
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	if err != nil {
		return errSuiteFailureStore
	}
	return nil
}
func (s *suiteFailureStore) unchanged() error {
	if s.root == nil || s.lock == nil {
		return errSuiteFailureStore
	}
	fresh, _, parent, e := openSessionReferenceDirectory(filepath.Join(s.path, "lock"))
	if e != nil {
		return errSuiteFailureStore
	}
	old, oldErr := s.root.Stat(".")
	closeErr := fresh.Close()
	base, baseErr := os.Lstat(filepath.Dir(s.path))
	lock, lockErr := s.root.Lstat("lock")
	if oldErr != nil || closeErr != nil || baseErr != nil || !safeSuiteFailureDirectory(base) || !safeSuiteFailureDirectory(parent) || !os.SameFile(old, parent) || lockErr != nil || !safeSuiteFailureFile(lock) || lock.Size() != 0 || !os.SameFile(lock, s.lockInfo) {
		return errSuiteFailureStore
	}
	return nil
}

// Pending bytes may be incomplete JSON. They are never read or displayed; only
// explicit whole-workspace clearing may remove this validated private staging file.
func (s *suiteFailureStore) pending() (*suiteFailureEntry, error) {
	info, err := s.root.Lstat(suiteFailurePendingName)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !safeSuiteFailureFile(info) {
		return nil, errSuiteFailureStore
	}
	return &suiteFailureEntry{name: suiteFailurePendingName, info: info}, nil
}

func (s *suiteFailureStore) scan() ([]suiteFailureEntry, error) {
	if s.unchanged() != nil {
		return nil, errSuiteFailureStore
	}
	dir, e := s.root.Open(".")
	if e != nil {
		return nil, errSuiteFailureStore
	}
	files, readErr := dir.ReadDir(maxSuiteFailureRecords + 3)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(files) > maxSuiteFailureRecords+2 {
		return nil, errSuiteFailureStore
	}
	var entries []suiteFailureEntry
	ids := map[string]bool{}
	sequences := map[uint64]bool{}
	for _, entry := range files {
		name := entry.Name()
		if name == "lock" {
			continue
		}
		if name == suiteFailurePendingName {
			if _, err := s.pending(); err != nil {
				return nil, err
			}
			continue
		}
		match := suiteFailureName.FindStringSubmatch(name)
		if match == nil {
			return nil, errSuiteFailureStore
		}
		info, e := s.root.Lstat(name)
		if e != nil || !safeSuiteFailureFile(info) {
			return nil, errSuiteFailureStore
		}
		directory, e := s.root.Open(".")
		if e != nil {
			return nil, errSuiteFailureStore
		}
		file, e := openKagentFile(directory, name)
		directoryErr := directory.Close()
		if e != nil || directoryErr != nil {
			if file != nil {
				_ = file.Close()
			}
			return nil, errSuiteFailureStore
		}
		opened, statErr := file.Stat()
		if statErr != nil || !safeSuiteFailureFile(opened) || !os.SameFile(info, opened) {
			_ = file.Close()
			return nil, errSuiteFailureStore
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxSuiteFailureRecordBytes+1))
		after, afterErr := file.Stat()
		closeErr := file.Close()
		now, nowErr := s.root.Lstat(name)
		if readErr != nil || afterErr != nil || closeErr != nil || nowErr != nil || !safeSuiteFailureFile(now) || !os.SameFile(opened, now) || !os.SameFile(opened, after) || opened.Size() != int64(len(raw)) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || len(raw) > maxSuiteFailureRecordBytes {
			return nil, errSuiteFailureStore
		}
		var record suiteFailureRecord
		var presence struct {
			Detail struct {
				Complete   *bool `json:"complete"`
				Attributed *bool `json:"attributed"`
			} `json:"detail"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if !evaluationJudgeJSONUnique(raw) || decoder.Decode(&record) != nil || json.Unmarshal(raw, &presence) != nil || presence.Detail.Complete == nil || presence.Detail.Attributed == nil || validateSuiteFailureRecord(record, nil) != nil {
			return nil, errSuiteFailureStore
		}
		sequence, e := strconv.ParseUint(match[1], 10, 64)
		if e != nil || sequence != record.Sequence || match[2] != record.Detail.ID || ids[match[2]] || sequences[sequence] {
			return nil, errSuiteFailureStore
		}
		ids[match[2]], sequences[sequence] = true, true
		entries = append(entries, suiteFailureEntry{name, now, record})
	}
	if s.unchanged() != nil || len(entries) > maxSuiteFailureRecords {
		return nil, errSuiteFailureStore
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].record.Sequence < entries[j].record.Sequence })
	return entries, nil
}

func validateSuiteFailureRecord(r suiteFailureRecord, keys []string) error {
	d := r.Detail
	if d.Verdict == "fail" && !d.Complete {
		return errSuiteFailureStore
	}
	if r.SchemaVersion != 1 || r.Sequence == 0 || !suiteFailureID.MatchString(d.ID) || !suiteFailureOperationID.MatchString(d.OperationID) || !suitePrefixedDigest(d.SuiteDigest) || d.Runtime != "orka" && d.Runtime != "kagent" && d.Runtime != "agentsessions" || d.Run < 1 || d.Run > d.Runs || d.Runs > 100 || d.MinPasses < 1 || d.MinPasses > d.Runs || d.OmittedAssertions < 0 || d.Verdict != "fail" && d.Verdict != "unknown" {
		return errSuiteFailureStore
	}
	check := func(s string, limit int) bool {
		safe, ok := suiteDiagnosticText(s, limit, keys)
		return ok && safe == s
	}
	identity := func(s string, limit int) bool { return check(s, limit) && suiteDiagnosticIdentity(s, keys) == s }
	if d.RetentionUnavailable || d.CaseID == "" || d.MemberID == "" || !identity(d.CaseID, 256) || !identity(d.MemberID, 256) || !identity(d.Class, 80) || !check(d.Answer, 64<<10) {
		return errSuiteFailureStore
	}
	switch d.AnswerStatus {
	case "complete", "truncated":
	case "unavailable", "withheld", "omitted":
		if d.Answer != "" {
			return errSuiteFailureStore
		}
	default:
		return errSuiteFailureStore
	}
	if !d.Attributed && d.Answer != "" {
		return errSuiteFailureStore
	}
	if len(d.Assertions) > 4096 {
		return errSuiteFailureStore
	}
	excerptBytes := 0
	for _, a := range d.Assertions {
		excerptBytes += len(a.Excerpt)
		if excerptBytes > 2048 {
			return errSuiteFailureStore
		}
		if a.ID == "" || a.Type == "" || a.Verdict != "fail" && a.Verdict != "unknown" || !identity(a.ID, 256) || !identity(a.Type, 256) || !identity(a.Class, 80) || !check(a.Expected, 512) || !check(a.Excerpt, 512) || len(a.Reasons) > 2 || len(a.Scores) != 0 && len(a.Scores) != 2 {
			return errSuiteFailureStore
		}
		if a.ExcerptKind != "" && a.ExcerptKind != "leading" && a.ExcerptKind != "match" {
			return errSuiteFailureStore
		}
		if !d.Attributed && (a.Excerpt != "" || len(a.Reasons) != 0) {
			return errSuiteFailureStore
		}
		if a.Threshold != nil && (*a.Threshold < 0 || *a.Threshold > 100) {
			return errSuiteFailureStore
		}
		for _, score := range a.Scores {
			if score < 0 || score > 100 {
				return errSuiteFailureStore
			}
		}
		for _, reason := range a.Reasons {
			if !check(reason, 512) {
				return errSuiteFailureStore
			}
		}
	}
	return nil
}

func marshalSuiteFailureRecord(r suiteFailureRecord, keys []string) ([]byte, error) {
	if validateSuiteFailureRecord(r, keys) != nil {
		return nil, errSuiteFailureStore
	}
	r.Detail.Assertions = append([]suiteFailureAssertion(nil), r.Detail.Assertions...)
	encode := func() []byte { raw, _ := json.Marshal(r); return raw }
	raw := encode()
	if len(raw) > maxSuiteFailureRecordBytes {
		r.Detail.ContentTruncated = true
		r.Detail.Answer = ""
		r.Detail.AnswerStatus = "omitted"
		raw = encode()
		// Keep the longest content prefix at each priority. Binary search avoids
		// repeatedly serializing megabytes once per assertion under hostile input.
		for phase := 0; phase < 3 && len(raw) > maxSuiteFailureRecordBytes; phase++ {
			original := append([]suiteFailureAssertion(nil), r.Detail.Assertions...)
			apply := func(keep int) {
				r.Detail.Assertions = append([]suiteFailureAssertion(nil), original...)
				if phase == 2 {
					r.Detail.OmittedAssertions += len(original) - keep
					r.Detail.Assertions = r.Detail.Assertions[:keep]
					return
				}
				for i := keep; i < len(original); i++ {
					a := &r.Detail.Assertions[i]
					if phase == 0 {
						a.Excerpt, a.ExcerptKind = "", ""
					} else {
						a.Reasons = nil
						a.Expected = ""
					}
				}
			}
			previousOmitted := r.Detail.OmittedAssertions
			lo, hi := 0, len(original)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				r.Detail.OmittedAssertions = previousOmitted
				apply(mid)
				if len(encode()) <= maxSuiteFailureRecordBytes {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			r.Detail.OmittedAssertions = previousOmitted
			apply(lo)
			raw = encode()
		}
	}
	if len(raw) > maxSuiteFailureRecordBytes || secretshapes.Match(string(raw)) != nil {
		return nil, errSuiteFailureStore
	}
	for _, key := range keys {
		if key != "" && strings.Contains(string(raw), key) {
			return nil, errSuiteFailureStore
		}
	}
	return raw, nil
}

func (s *suiteFailureStore) remove(entry suiteFailureEntry) error {
	if s.unchanged() != nil {
		return errSuiteFailureStore
	}
	now, e := s.root.Lstat(entry.name)
	if e != nil || !safeSuiteFailureFile(now) || !os.SameFile(now, entry.info) || now.Size() != entry.info.Size() || !now.ModTime().Equal(entry.info.ModTime()) {
		return errSuiteFailureStore
	}
	if s.root.Remove(entry.name) != nil {
		return errSuiteFailureStore
	}
	return nil
}
func (s *suiteFailureStore) save(ctx context.Context, d suiteFailureDetail) (id string, err error) {
	entries, e := s.scan()
	if e != nil {
		return "", e
	}
	if pending, err := s.pending(); err != nil || pending != nil {
		return "", errSuiteFailureStore
	}
	var sequence uint64 = 1
	if len(entries) > 0 {
		sequence = entries[len(entries)-1].record.Sequence + 1
		if sequence == 0 {
			return "", errSuiteFailureStore
		}
	}
	id, e = randomHex(16)
	if e != nil {
		return "", errSuiteFailureStore
	}
	id = "f-" + id
	d.ID = id
	var keys []string
	if reporter, ok := ctx.Value(suiteFailureReportingKey{}).(suiteFailureReporter); ok {
		keys = []string{reporter.key}
	}
	raw, e := marshalSuiteFailureRecord(suiteFailureRecord{1, sequence, d}, keys)
	if e != nil {
		return "", e
	}
	if len(entries) == maxSuiteFailureRecords {
		if s.remove(entries[0]) != nil {
			return "", errSuiteFailureStore
		}
	}
	if s.unchanged() != nil {
		return "", errSuiteFailureStore
	}
	const stage = suiteFailurePendingName
	file, e := s.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", errSuiteFailureStore
	}
	info, statErr := file.Stat()
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			now, e := s.root.Lstat(stage)
			if statErr == nil && e == nil && os.SameFile(info, now) && safeSuiteFailureFile(now) {
				if s.root.Remove(stage) != nil {
					err = errSuiteFailureStore
				}
			}
		}
	}()
	if statErr != nil || !safeSuiteFailureFile(info) {
		return "", errSuiteFailureStore
	}
	n, writeErr := s.writeFile(file, raw)
	syncErr := file.Sync()
	closeErr := s.closeFile(file)
	if writeErr != nil || n != len(raw) || syncErr != nil || closeErr != nil || s.unchanged() != nil {
		return "", errSuiteFailureStore
	}
	name := fmt.Sprintf("%020d-%s.json", sequence, id)
	if _, e := s.root.Lstat(name); !os.IsNotExist(e) {
		return "", errSuiteFailureStore
	}
	current, e := s.root.Lstat(stage)
	if e != nil || !safeSuiteFailureFile(current) || !os.SameFile(info, current) || current.Size() != int64(len(raw)) {
		return "", errSuiteFailureStore
	}
	if s.rename(stage, name) != nil {
		return "", errSuiteFailureStore
	}
	published = true
	directory, e := s.root.Open(".")
	if e != nil {
		return id, errSuiteFailureStore
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil || closeErr != nil || s.unchanged() != nil {
		return id, errSuiteFailureStore
	}
	return id, nil
}
func saveSuiteFailure(ctx context.Context, workspaceRoot string, d suiteFailureDetail) (string, error) {
	s, e := openSuiteFailureStore(workspaceRoot, true)
	if e != nil {
		return "", e
	}
	id, saveErr := s.save(ctx, d)
	closeErr := s.Close()
	if saveErr != nil {
		return id, saveErr
	}
	if closeErr != nil {
		return id, closeErr
	}
	return id, nil
}
func clearSuiteFailures(ctx context.Context, workspaceRoot, id string) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if id != "" && !suiteFailureID.MatchString(id) {
		return 0, errSuiteFailureStore
	}
	s, e := openSuiteFailureStore(workspaceRoot, false)
	if e != nil {
		return 0, e
	}
	defer s.Close()
	entries, e := s.scan()
	if e != nil {
		return 0, e
	}
	if id == "" {
		pending, err := s.pending()
		if err != nil {
			return 0, err
		}
		if pending != nil {
			entries = append(entries, *pending)
		}
	}
	removed := 0
	for _, entry := range entries {
		if id == "" || entry.record.Detail.ID == id {
			if s.remove(entry) != nil {
				return removed, errSuiteFailureStore
			}
			removed++
		}
	}
	if id != "" && removed == 0 {
		return 0, errSuiteFailureMissing
	}
	directory, e := s.root.Open(".")
	if e != nil {
		return removed, errSuiteFailureStore
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil || s.unchanged() != nil {
		return removed, errSuiteFailureStore
	}
	return removed, nil
}
