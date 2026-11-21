package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

const (
	maxSuitePassRecords        = 256
	maxSuitePassRecordBytes    = maxSuitePassingReceiptBytes + 4096
	maxSuitePassPartitionBytes = 16 << 20
)

var (
	errSuitePassStore   = errors.New("managed passing evidence unavailable or unsafe; inspect private KMX state, or select --evaluation FILE explicitly")
	errSuitePassMissing = errors.New("no completed passing evidence for this exact suite; run kmx eval, select --evaluation FILE, or explicitly push --unevaluated")
	// errSuitePassSessionsV1 is the latest record holding an AgentSessions
	// passing receipt v1. It is not proof and older records are not selected
	// instead; a new evaluation commits after it.
	errSuitePassSessionsV1 = errors.New("latest managed AgentSessions passing evidence for this exact suite is receipt v1, which does not record an AgentSessionsBinding, and is not eligible; run kmx package test --development-host FILE --bindings FILE again, select --evaluation FILE, or explicitly push --unevaluated")
	suitePassRecordName    = regexp.MustCompile(`^[0-9]{20}\.json$`)
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
	directory                                                *fileaccess.Directory
	privateLock                                              *fileaccess.PrivateFile
	privateLockID                                            string
	completion                                               *suitePassingExport
}

func (s *suitePassStore) unchanged() error {
	if s.directory != nil || s.privateLock != nil {
		return s.unchangedChecked()
	}
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
	if s.privateLock != nil {
		err = errors.Join(err, s.privateLock.Close())
		s.privateLock = nil
	}
	if s.directory != nil {
		err = errors.Join(err, s.directory.Close())
		s.directory = nil
	}
	return err
}

func (s *suitePassStore) unchangedChecked() error {
	if s.directory == nil || s.privateLock == nil || s.privateLockID == "" {
		return errSuitePassStore
	}
	fresh, err := fileaccess.OpenPrivateDirectory(context.Background(), s.path)
	if err != nil {
		return errSuitePassStore
	}
	wantDirectory, wantErr := fileaccess.PhysicalIdentity(context.Background(), s.directory)
	gotDirectory, gotErr := fileaccess.PhysicalIdentity(context.Background(), fresh)
	freshCloseErr := fresh.Close()
	lock, lockErr := fileaccess.OpenPrivateFileAt(context.Background(), s.directory, "lock")
	if wantErr != nil || gotErr != nil || freshCloseErr != nil || lockErr != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return errSuitePassStore
	}
	lockID, identityErr := fileaccess.PhysicalIdentity(context.Background(), lock)
	info, statErr := lock.Stat()
	links, linksErr := fileaccess.LinkCount(context.Background(), lock)
	lockCloseErr := lock.Close()
	if wantDirectory != gotDirectory || identityErr != nil || statErr != nil || linksErr != nil || lockCloseErr != nil || lockID != s.privateLockID || info.Size() != 0 || links != 1 {
		return errSuitePassStore
	}
	return nil
}

type suitePassScan struct {
	latest           *suitePassingReceipt
	path             string
	sequence         uint64 // global allocation maximum
	selectedSequence uint64 // latest coherent receipt for the selected composition
	count, bytes     int
	// sessionsV1 marks the selected record as an ineligible AgentSessions
	// passing receipt v1; latest is then nil.
	sessionsV1 bool
}

func (s *suitePassStore) scan(ctx context.Context, suite *workspace.NativeSuite) (result suitePassScan, err error) {
	if err := s.unchanged(); err != nil {
		return result, err
	}
	if s.directory != nil {
		return s.scanChecked(ctx, suite)
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
			if e := s.consumePassRecord(ctx, suite, name, raw, &result); e != nil {
				return result, e
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

func (s *suitePassStore) scanChecked(ctx context.Context, suite *workspace.NativeSuite) (result suitePassScan, err error) {
	directory, err := fileaccess.OpenPrivateDirectory(ctx, s.partition)
	if err != nil {
		return result, errSuitePassStore
	}
	identity, err := fileaccess.PhysicalIdentity(ctx, directory)
	if err != nil {
		_ = directory.Close()
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
		entries, readErr := directory.ReadDir(64)
		if readErr != nil {
			return result, errSuitePassStore
		}
		for _, entry := range entries {
			name := entry.Name()
			if !suitePassRecordName.MatchString(name) {
				continue
			}
			file, openErr := fileaccess.OpenPrivateFileAt(ctx, directory, name)
			if openErr != nil {
				return result, errSuitePassStore
			}
			before, statErr := file.Stat()
			fileIdentity, identityErr := fileaccess.PhysicalIdentity(ctx, file)
			if statErr != nil || identityErr != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxSuitePassRecordBytes {
				_ = file.Close()
				return result, errSuitePassStore
			}
			raw, readErr := io.ReadAll(io.LimitReader(file, maxSuitePassRecordBytes+1))
			after, afterErr := file.Stat()
			closeErr := file.Close()
			current, currentErr := fileaccess.OpenPrivateFileAt(ctx, directory, name)
			var currentIdentity string
			var currentInfo os.FileInfo
			if currentErr == nil {
				currentIdentity, currentErr = fileaccess.PhysicalIdentity(ctx, current)
				if currentErr == nil {
					currentInfo, currentErr = current.Stat()
				}
				currentErr = errors.Join(currentErr, current.Close())
			}
			if readErr != nil || afterErr != nil || closeErr != nil || currentErr != nil || currentInfo == nil || currentIdentity != fileIdentity || len(raw) > maxSuitePassRecordBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || currentInfo.Size() != before.Size() || currentInfo.Mode() != before.Mode() || !currentInfo.ModTime().Equal(before.ModTime()) {
				return result, errSuitePassStore
			}
			if err := s.consumePassRecord(ctx, suite, name, raw, &result); err != nil {
				return result, err
			}
		}
		if len(entries) == 0 {
			break
		}
	}
	fresh, err := fileaccess.OpenPrivateDirectory(ctx, s.partition)
	if err != nil {
		return result, errSuitePassStore
	}
	now, identityErr := fileaccess.PhysicalIdentity(ctx, fresh)
	closeErr := fresh.Close()
	if identityErr != nil || closeErr != nil || now != identity {
		return result, errSuitePassStore
	}
	if err := s.unchanged(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *suitePassStore) consumePassRecord(ctx context.Context, suite *workspace.NativeSuite, name string, raw []byte, result *suitePassScan) error {
	result.count++
	if result.count > maxSuitePassRecords {
		return errSuitePassStore
	}
	seq, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	if err != nil || seq == 0 {
		return errSuitePassStore
	}
	result.bytes += len(raw)
	if result.bytes > maxSuitePassPartitionBytes {
		return errSuitePassStore
	}
	var record suitePassRecord
	if suitecore.Unmarshal(raw, &record) != nil || record.SchemaVersion != 1 || record.Workspace != s.identity || record.SuiteDigest != s.suiteDigest || record.Sequence != seq {
		return errSuitePassStore
	}
	receipt, sessionsV1, parseErr := classifySuitePassingReceipt(record.Receipt)
	if sessionsV1 {
		if receipt.suiteDigest() != s.suiteDigest {
			return errSuitePassStore
		}
		if seq > result.sequence {
			result.sequence = seq
		}
		if suite != nil {
			own, err := suite.ForCompositionDigest(ctx, receipt.compositionDigest())
			if err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				return errSuitePassStore
			}
			if _, err := canonicalSuitePassingReceipt(ctx, receipt, own); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				return errSuitePassStore
			}
			if own.CompositionDigest != suite.CompositionDigest {
				return nil
			}
		}
		if seq > result.selectedSequence {
			result.selectedSequence, result.latest, result.path, result.sessionsV1 = seq, nil, filepath.Join(s.partition, name), true
		}
		return nil
	}
	if parseErr != nil || receipt.suiteDigest() != s.suiteDigest {
		return errSuitePassStore
	}
	if seq > result.sequence {
		result.sequence = seq
	}
	if suite != nil {
		own, err := suite.ForCompositionDigest(ctx, receipt.compositionDigest())
		if err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return errSuitePassStore
		}
		if _, err := canonicalSuitePassingReceipt(ctx, receipt, own); err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return errSuitePassStore
		}
		if own.CompositionDigest != suite.CompositionDigest {
			return nil
		}
	}
	if seq > result.selectedSequence {
		result.selectedSequence, result.latest, result.path, result.sessionsV1 = seq, receipt, filepath.Join(s.partition, name), false
	}
	return nil
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
	if scan.sessionsV1 {
		return nil, "", errSuitePassSessionsV1
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
	encoded, err := suitecore.Marshal(record)
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
	if err != nil && output.privateDirectory != nil && !output.visibilityKnown {
		return output.path, fmt.Errorf("completed managed proof visibility is unresolved at %q; inspect before retrying: %w", output.path, err)
	}
	if err != nil {
		return "", err
	}
	return output.path, nil
}
