package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
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
	name     string
	identity string
	info     os.FileInfo
	links    uint64
	record   suiteFailureRecord
}
type suiteFailureStore struct {
	directory *fileaccess.Directory
	lock      *fileaccess.PrivateFile
	lockID    string
	path      string
	publish   func(context.Context, *fileaccess.Directory, string, []byte, string) (fileaccess.PublicationResult, error)
	syncDir   func(context.Context, *fileaccess.Directory) error
}

func (s *suiteFailureStore) Close() error {
	var err error
	if s.lock != nil {
		err = s.lock.Close()
		s.lock = nil
	}
	if s.directory != nil {
		err = errors.Join(err, s.directory.Close())
		s.directory = nil
	}
	if err != nil {
		return errSuiteFailureStore
	}
	return nil
}
func (s *suiteFailureStore) unchanged() error {
	if s.directory == nil || s.lock == nil || s.lockID == "" {
		return errSuiteFailureStore
	}
	fresh, e := fileaccess.OpenPrivateDirectory(context.Background(), s.path)
	if e != nil {
		return errSuiteFailureStore
	}
	wantDir, oldErr := fileaccess.PhysicalIdentity(context.Background(), s.directory)
	gotDir, freshErr := fileaccess.PhysicalIdentity(context.Background(), fresh)
	closeErr := fresh.Close()
	lock, lockErr := fileaccess.OpenPrivateFileAt(context.Background(), s.directory, "lock")
	if oldErr != nil || freshErr != nil || closeErr != nil || lockErr != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return errSuiteFailureStore
	}
	lockID, identityErr := fileaccess.PhysicalIdentity(context.Background(), lock)
	info, statErr := lock.Stat()
	links, linksErr := fileaccess.LinkCount(context.Background(), lock)
	lockCloseErr := lock.Close()
	if wantDir != gotDir || identityErr != nil || statErr != nil || linksErr != nil || lockCloseErr != nil || lockID != s.lockID || info.Size() != 0 || links != 1 {
		return errSuiteFailureStore
	}
	return nil
}

// Pending bytes may be incomplete JSON. They are never read or displayed; only
// explicit whole-workspace clearing may remove this validated private staging file.
func (s *suiteFailureStore) pending() (*suiteFailureEntry, error) {
	file, err := fileaccess.OpenPrivateFileAt(context.Background(), s.directory, suiteFailurePendingName)
	if errors.Is(err, fileaccess.ErrMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	info, statErr := file.Stat()
	identity, identityErr := fileaccess.PhysicalIdentity(context.Background(), file)
	links, linksErr := fileaccess.LinkCount(context.Background(), file)
	closeErr := file.Close()
	if statErr != nil || identityErr != nil || linksErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Size() > maxSuiteFailureRecordBytes || links < 1 || links > 2 {
		return nil, errSuiteFailureStore
	}
	return &suiteFailureEntry{name: suiteFailurePendingName, identity: identity, info: info, links: links}, nil
}

func (s *suiteFailureStore) scan() ([]suiteFailureEntry, error) {
	if s.unchanged() != nil {
		return nil, errSuiteFailureStore
	}
	scanDirectory, e := fileaccess.OpenPrivateDirectory(context.Background(), s.path)
	if e != nil {
		return nil, errSuiteFailureStore
	}
	wantDirectory, wantErr := fileaccess.PhysicalIdentity(context.Background(), s.directory)
	gotDirectory, gotErr := fileaccess.PhysicalIdentity(context.Background(), scanDirectory)
	if wantErr != nil || gotErr != nil || wantDirectory != gotDirectory {
		_ = scanDirectory.Close()
		return nil, errSuiteFailureStore
	}
	files, readErr := scanDirectory.ReadDir(maxSuiteFailureRecords + 3)
	closeErr := scanDirectory.Close()
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
		raw, identity, info, links, e := s.readEntry(name, true)
		if e != nil || identity == "" {
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
		if !agentruntime.JSONKeysUnique(raw) || decoder.Decode(&record) != nil || json.Unmarshal(raw, &presence) != nil || presence.Detail.Complete == nil || presence.Detail.Attributed == nil || validateSuiteFailureRecord(record, nil) != nil {
			return nil, errSuiteFailureStore
		}
		sequence, e := strconv.ParseUint(match[1], 10, 64)
		if e != nil || sequence != record.Sequence || match[2] != record.Detail.ID || ids[match[2]] || sequences[sequence] {
			return nil, errSuiteFailureStore
		}
		ids[match[2]], sequences[sequence] = true, true
		entries = append(entries, suiteFailureEntry{name: name, identity: identity, info: info, links: links, record: record})
	}
	pending, pendingErr := s.pending()
	if pendingErr != nil {
		return nil, pendingErr
	}
	if pending != nil && pending.links == 2 {
		matched := 0
		for _, entry := range entries {
			switch {
			case entry.links == 2 && entry.identity == pending.identity:
				matched++
			case entry.links != 1:
				return nil, errSuiteFailureStore
			}
		}
		if matched != 1 {
			return nil, errSuiteFailureStore
		}
	} else {
		for _, entry := range entries {
			if entry.links != 1 {
				return nil, errSuiteFailureStore
			}
		}
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
	file, e := fileaccess.OpenPrivateFileAt(context.Background(), s.directory, entry.name)
	if e != nil {
		return errSuiteFailureStore
	}
	now, statErr := file.Stat()
	identity, identityErr := fileaccess.PhysicalIdentity(context.Background(), file)
	if statErr != nil || identityErr != nil || identity != entry.identity || now.Size() != entry.info.Size() || !now.ModTime().Equal(entry.info.ModTime()) {
		_ = file.Close()
		return errSuiteFailureStore
	}
	removeErr := fileaccess.RemovePrivateFileAt(context.Background(), s.directory, entry.name, file)
	closeErr := file.Close()
	if removeErr != nil || closeErr != nil {
		return errSuiteFailureStore
	}
	return nil
}

func (s *suiteFailureStore) removeOwnPair(pending suiteFailureEntry, entries []suiteFailureEntry) (bool, error) {
	if pending.links != 2 {
		return false, nil
	}
	var final *suiteFailureEntry
	for i := range entries {
		if entries[i].links != 2 {
			continue
		}
		if entries[i].identity == pending.identity {
			if final != nil {
				return false, errSuiteFailureStore
			}
			final = &entries[i]
		}
	}
	if final == nil {
		return false, errSuiteFailureStore
	}
	if s.remove(pending) != nil {
		return false, errSuiteFailureStore
	}
	final.links = 1
	return true, nil
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
	name := fmt.Sprintf("%020d-%s.json", sequence, id)
	result, publishErr := s.publish(ctx, s.directory, name, raw, suiteFailurePendingName)
	if publishErr != nil {
		if result.Visible {
			return id, errSuiteFailureStore
		}
		// Not positively observed is not proof of absence. Keep the generated ID
		// whenever publication reconciliation could not establish either state.
		if !result.VisibilityKnown {
			return id, errSuiteFailureStore
		}
		return "", errSuiteFailureStore
	}
	if !result.Visible || !result.VisibilityKnown || s.unchanged() != nil {
		return id, errSuiteFailureStore
	}
	if s.syncDir(ctx, s.directory) != nil {
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
			if recovered, recoverErr := s.removeOwnPair(*pending, entries); recoverErr != nil {
				return 0, recoverErr
			} else if recovered {
				pending = nil
			}
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
	if s.syncDir(ctx, s.directory) != nil || s.unchanged() != nil {
		return removed, errSuiteFailureStore
	}
	return removed, nil
}
