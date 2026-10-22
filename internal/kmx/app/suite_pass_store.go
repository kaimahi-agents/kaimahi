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
	errSuitePassSessionsV1 = errors.New("latest managed AgentSessions passing evidence for this exact suite is receipt v1, which does not record an AgentSessionsBinding, and is not eligible; run kmx test --development-host FILE --bindings FILE again, select --evaluation FILE, or explicitly push --unevaluated")
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
	completion                                               *suitePassingExport
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
	// sessionsV1 marks the selected record as an ineligible AgentSessions
	// passing receipt v1; latest is then nil.
	sessionsV1 bool
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
			if suitecore.Unmarshal(raw, &record) != nil || record.SchemaVersion != 1 || record.Workspace != s.identity || record.SuiteDigest != s.suiteDigest || record.Sequence != seq {
				return result, errSuitePassStore
			}
			receipt, sessionsV1, e := classifySuitePassingReceipt(record.Receipt)
			if sessionsV1 {
				// Counted for capacity and ordering, never eligible. Apart from
				// its version and placeholder binding digest it must be coherent
				// with its own composition of this suite, like any other record;
				// only the selected composition's record blocks lookup.
				if receipt.suiteDigest() != s.suiteDigest {
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
					result.selectedSequence, result.latest, result.path, result.sessionsV1 = seq, nil, filepath.Join(s.partition, name), true
				}
				continue
			}
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
				result.selectedSequence, result.latest, result.path, result.sessionsV1 = seq, receipt, filepath.Join(s.partition, name), false
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
	if err != nil {
		return "", err
	}
	return output.path, nil
}
