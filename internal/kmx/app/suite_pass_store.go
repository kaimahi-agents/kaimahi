package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"golang.org/x/sys/unix"
)

const (
	maxSuitePassRecords        = 256
	maxSuitePassRecordBytes    = maxSuitePassingReceiptBytes + 4096
	maxSuitePassPartitionBytes = 16 << 20
)

var (
	errSuitePassStore   = errors.New("managed passing evidence unavailable or unsafe; inspect private KMX state, or select --evaluation FILE explicitly")
	errSuitePassMissing = errors.New("no completed passing evidence for this exact suite; run kmx eval, select --evaluation FILE, or explicitly push --unevaluated")
	suitePassRecordName = regexp.MustCompile(`^[0-9]{20}\.json$`)
)

// The embedded receipt retains its existing private wire format. Directory and
// record identities scope lookup; neither unsigned file establishes authenticity.
type suitePassRecord struct {
	SchemaVersion int             `json:"schemaVersion"`
	Workspace     string          `json:"workspace"`
	SuiteDigest   string          `json:"suiteDigest"`
	Sequence      uint64          `json:"sequence"`
	Receipt       json.RawMessage `json:"receipt"`
}

type suitePassStore struct {
	workspace, cache, path, partition, identity, suiteDigest string
	lock                                                     *os.File
	lockInfo                                                 os.FileInfo
	root                                                     *os.Root
	completion                                               *suitePassingExport
}

// Creation walks anchored directories without following links or changing
// existing permissions. Only managed children must be private; existing KMX
// settings directories can retain their normal, non-writable-to-others modes.
func ensureSuitePassDirectory(path string, private bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errSuitePassStore
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return errSuitePassStore
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if os.IsNotExist(err) {
			if err := root.Mkdir(part, 0700); err != nil && !os.IsExist(err) {
				root.Close()
				return errSuitePassStore
			}
			info, err = root.Lstat(part)
		}
		if err != nil || !safeSessionReferenceDirectory(info, false) {
			root.Close()
			return errSuitePassStore
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return errSuitePassStore
		}
		opened, openErr := next.Stat(".")
		current, currentErr := root.Lstat(part)
		closeErr := root.Close()
		if openErr != nil || currentErr != nil || closeErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) {
			next.Close()
			return errSuitePassStore
		}
		root = next
	}
	info, err := root.Stat(".")
	closeErr := root.Close()
	if err != nil || closeErr != nil || !safeSessionReferenceDirectory(info, true) || private && info.Mode().Perm()&0077 != 0 {
		return errSuitePassStore
	}
	return nil
}

func openSuitePassStore(ctx context.Context, workspaceRoot, cache, suiteDigest string) (_ *suitePassStore, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !suitePrefixedDigest(suiteDigest) {
		return nil, errSuitePassStore
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuitePassStore
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return nil, errSuitePassStore
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(workspaceRoot)))
	base := filepath.Join(state, "passing")
	path := filepath.Join(base, identity)
	if scaffold.RefuseKeyShapes(path) != nil || suiteReceiptInside(path, workspaceRoot) || suiteReceiptInside(path, cache) {
		return nil, errSuitePassStore
	}
	for i, p := range []string{state, base, path} {
		if err := ensureSuitePassDirectory(p, i > 0); err != nil {
			return nil, err
		}
	}
	root, _, _, err := openSessionReferenceDirectory(filepath.Join(path, "lock"))
	if err != nil {
		return nil, errSuitePassStore
	}
	s := &suitePassStore{workspace: workspaceRoot, cache: cache, path: path, partition: filepath.Join(path, strings.TrimPrefix(suiteDigest, "sha256:")), identity: identity, suiteDigest: suiteDigest, root: root}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	lock, err := root.OpenFile("lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		directory, e := root.Open(".")
		if e != nil {
			return nil, errSuitePassStore
		}
		fd, e := unix.Openat(int(directory.Fd()), "lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		closeErr := directory.Close()
		if e != nil || closeErr != nil {
			if e == nil {
				unix.Close(fd)
			}
			return nil, errSuitePassStore
		}
		lock = os.NewFile(uintptr(fd), "lock")
		err = nil
	}
	if err != nil {
		return nil, errSuitePassStore
	}
	s.lock = lock
	s.lockInfo, err = lock.Stat()
	if err != nil || !s.lockInfo.Mode().IsRegular() || s.lockInfo.Mode().Perm()&0077 != 0 {
		return nil, errSuitePassStore
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("another workspace eval or push is active; retry after it exits (no stale lock deletion is needed)")
	}
	if err := ensureSuitePassDirectory(s.partition, true); err != nil {
		return nil, err
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *suitePassStore) unchanged() error {
	if s.lock == nil || s.root == nil {
		return errSuitePassStore
	}
	current, err := s.root.Lstat("lock")
	fresh, _, _, openErr := openSessionReferenceDirectory(filepath.Join(s.path, "lock"))
	if openErr != nil {
		return errSuitePassStore
	}
	before, beforeErr := s.root.Stat(".")
	now, nowErr := fresh.Stat(".")
	closeErr := fresh.Close()
	if err != nil || beforeErr != nil || nowErr != nil || closeErr != nil || !os.SameFile(before, now) || now.Mode().Perm()&0077 != 0 || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !os.SameFile(current, s.lockInfo) {
		return errSuitePassStore
	}
	return nil
}

func (s *suitePassStore) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.completion != nil {
		err = errors.Join(err, s.completion.Close())
		s.completion = nil
	}
	// Never unlink the lock: flock authority belongs to this open description.
	if s.lock != nil {
		err = errors.Join(err, s.lock.Close())
		s.lock = nil
	}
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
		s.root = nil
	}
	return err
}

type suitePassScan struct {
	latest           *suitePassingReceipt
	path             string
	sequence         uint64 // global allocation maximum
	selectedSequence uint64 // latest coherent receipt for the selected composition
	count, bytes     int
}

func (s *suitePassStore) scan(ctx context.Context, suite *workspace.NativeSuite) (result suitePassScan, err error) {
	if err := s.unchanged(); err != nil {
		return result, err
	}
	root, _, parent, err := openSessionReferenceDirectory(filepath.Join(s.partition, "entry"))
	if err != nil || parent.Mode().Perm()&0077 != 0 {
		if root != nil {
			root.Close()
		}
		return result, errSuitePassStore
	}
	defer func() {
		if root.Close() != nil {
			err = errors.Join(err, errSuitePassStore)
		}
	}()
	directory, err := root.Open(".")
	if err != nil {
		return result, errSuitePassStore
	}
	defer func() {
		if directory.Close() != nil {
			err = errors.Join(err, errSuitePassStore)
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		names, readErr := directory.Readdirnames(64)
		for _, name := range names {
			// Uncommitted output/stages have no eligibility and consume no record slots.
			if !suitePassRecordName.MatchString(name) {
				continue
			}
			result.count++
			if result.count > maxSuitePassRecords {
				return result, errSuitePassStore
			}
			seq, e := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
			if e != nil || seq == 0 {
				return result, errSuitePassStore
			}
			file, e := openKagentFile(directory, name)
			if e != nil {
				return result, errSuitePassStore
			}
			info, e := file.Stat()
			if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxSuitePassRecordBytes {
				file.Close()
				return result, errSuitePassStore
			}
			raw, e := io.ReadAll(io.LimitReader(file, maxSuitePassRecordBytes+1))
			after, afterErr := file.Stat()
			closeErr := file.Close()
			current, currentErr := root.Lstat(name)
			if e != nil || afterErr != nil || closeErr != nil || currentErr != nil || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !os.SameFile(info, current) || after.Size() != info.Size() || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) || int64(len(raw)) != info.Size() || len(raw) > maxSuitePassRecordBytes {
				return result, errSuitePassStore
			}
			result.bytes += len(raw)
			if result.bytes > maxSuitePassPartitionBytes {
				return result, errSuitePassStore
			}
			var record suitePassRecord
			if agentsuite.Unmarshal(raw, &record) != nil || record.SchemaVersion != 1 || record.Workspace != s.identity || record.SuiteDigest != s.suiteDigest || record.Sequence != seq {
				return result, errSuitePassStore
			}
			receipt, e := parseSuitePassingReceipt(record.Receipt)
			if e != nil || receipt.suiteDigest() != s.suiteDigest {
				return result, errSuitePassStore
			}
			if seq > result.sequence {
				result.sequence = seq
			}
			if suite != nil {
				own, err := suite.ForCompositionDigest(ctx, receipt.compositionDigest())
				if err != nil {
					if err := ctx.Err(); err != nil {
						return result, err
					}
					return result, errSuitePassStore
				}
				if _, err := canonicalSuitePassingReceipt(ctx, receipt, own); err != nil {
					if err := ctx.Err(); err != nil {
						return result, err
					}
					return result, errSuitePassStore
				}
				if own.CompositionDigest != suite.CompositionDigest {
					continue
				}
			}
			if seq > result.selectedSequence {
				result.selectedSequence = seq
				result.latest = receipt
				result.path = filepath.Join(s.partition, name)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, errSuitePassStore
		}
	}
	fresh, _, now, e := openSessionReferenceDirectory(filepath.Join(s.partition, "entry"))
	if e != nil {
		return result, errSuitePassStore
	}
	if fresh.Close() != nil || !os.SameFile(parent, now) || now.Mode().Perm()&0077 != 0 {
		return result, errSuitePassStore
	}
	if err := s.unchanged(); err != nil {
		return result, err
	}
	return result, nil
}

// reserve is evaluation-only and runs before runtime settings or effects. Lookup
// deliberately does not require a free slot, so a full partition can still push.
func (s *suitePassStore) reserve(ctx context.Context) error {
	if s.completion != nil {
		return errSuitePassStore
	}
	scan, err := s.scan(ctx, nil)
	if err != nil {
		return err
	}
	if scan.count >= maxSuitePassRecords || scan.bytes+maxSuitePassRecordBytes > maxSuitePassPartitionBytes || scan.sequence == ^uint64(0) {
		return errors.New("managed evidence partition is full; existing proof remains usable; select a different private KMX_HOME for new evaluations")
	}
	filename := filepath.Join(s.partition, fmt.Sprintf("%020d.json", scan.sequence+1))
	s.completion, err = preflightSuitePassingExport(ctx, filename, s.workspace, s.cache)
	return err
}

func (s *suitePassStore) latest(ctx context.Context, suite *workspace.NativeSuite) (*suitePassingReceipt, string, error) {
	if suite == nil || suite.Digest != s.suiteDigest {
		return nil, "", errSuitePassStore
	}
	scan, err := s.scan(ctx, suite)
	if err != nil {
		return nil, "", err
	}
	if scan.latest == nil {
		return nil, "", errSuitePassMissing
	}
	return scan.latest, scan.path, nil
}

func (s *suitePassStore) commit(ctx context.Context, raw []byte, suite *workspace.NativeSuite) (string, error) {
	if err := suite.RequireUnprofiled(); err != nil {
		return "", err
	}
	if s.completion == nil || suite == nil || suite.Digest != s.suiteDigest {
		return "", errSuitePassStore
	}
	receipt, err := parseSuitePassingReceipt(raw)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalSuitePassingReceipt(ctx, receipt, suite)
	if err != nil {
		return "", err
	}
	if err := s.unchanged(); err != nil {
		return "", err
	}
	output := s.completion
	s.completion = nil
	seq, err := strconv.ParseUint(strings.TrimSuffix(output.name, ".json"), 10, 64)
	if err != nil {
		output.Close()
		return "", errSuitePassStore
	}
	record := suitePassRecord{1, s.identity, s.suiteDigest, seq, canonical}
	encoded, err := agentsuite.Marshal(record)
	if err == nil && len(encoded) > maxSuitePassRecordBytes {
		err = errSuitePassStore
	}
	if err == nil {
		err = output.publishBytes(ctx, encoded)
	}
	err = errors.Join(err, output.Close())
	if err != nil && output.linked {
		return output.path, fmt.Errorf("completed managed proof may exist at %q; persistence uncertain: %w", output.path, err)
	}
	if err != nil {
		return "", err
	}
	return output.path, nil
}
