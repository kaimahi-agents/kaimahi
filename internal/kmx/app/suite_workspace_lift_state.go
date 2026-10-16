package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	agentsuite "github.com/agent-primitives/suite"
)

var errWorkspaceLiftState = errors.New("workspace Lift completion unavailable, changed or unsafe; refusing replay, adoption or recovery")

// Completion is immutable private skip authority, not Apply/evaluation evidence.
// There are at most 64 completion files per exact-S partition, each <=64 KiB.
type workspaceLiftRecord struct {
	Version    int                   `json:"version"`
	Workspace  string                `json:"workspace"`
	Suite      string                `json:"suite"`
	Repository string                `json:"repository"`
	Scope      string                `json:"scope"`
	Placement  *suiteNativePlacement `json:"placement,omitempty"`
}
type workspaceLiftCompletion struct {
	output   *suitePassingExport
	record   *workspaceLiftRecord
	expected workspaceLiftRecord
}

func (c *workspaceLiftCompletion) Close() error {
	if c.output != nil {
		return c.output.Close()
	}
	return nil
}
func (p *PreparedWorkspaceLift) liftRecord(ctx context.Context, kind, scope string) (*workspaceLiftCompletion, error) {
	s := p.store
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("lift-%s-%x.json", kind, sha256.Sum256([]byte(scope)))
	path := filepath.Join(s.partition, name)
	root, _, parent, err := openSessionReferenceDirectory(path)
	if err != nil {
		return nil, errWorkspaceLiftState
	}
	defer root.Close()
	if parent.Mode().Perm()&0077 != 0 {
		return nil, errWorkspaceLiftState
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, errWorkspaceLiftState
	}
	defer dir.Close()
	names, err := dir.Readdirnames(1024)
	if err != nil && err != io.EOF {
		return nil, errWorkspaceLiftState
	}
	count := 0
	for _, n := range names {
		if len(n) > 5 && n[:5] == "lift-" {
			count++
		}
	}
	if len(names) >= 1024 || count > 64 {
		return nil, errWorkspaceLiftState
	}
	c := &workspaceLiftCompletion{expected: workspaceLiftRecord{Version: 1, Workspace: s.identity, Suite: s.suiteDigest, Repository: p.options.Repository, Scope: kind + "\n" + scope}}
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		if count+p.completionSlots >= 64 {
			return nil, errWorkspaceLiftState
		}
		c.output, err = preflightSuitePassingExport(ctx, path, s.workspace, s.cache)
		if err == nil {
			p.completionSlots++
		}
		return c, err
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errWorkspaceLiftState
	}
	file, err := openKagentFile(dir, name)
	if err != nil {
		return nil, errWorkspaceLiftState
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, errWorkspaceLiftState
	}
	raw, err := readKagentOpenedFile(file, opened, true)
	closeErr := file.Close()
	now, e := root.Lstat(name)
	fresh, _, parentNow, e2 := openSessionReferenceDirectory(path)
	if e2 != nil {
		return nil, errWorkspaceLiftState
	}
	closeErr = errors.Join(closeErr, fresh.Close())
	if err != nil || closeErr != nil || e != nil || !os.SameFile(info, opened) || !os.SameFile(info, now) || !os.SameFile(parent, parentNow) || parentNow.Mode().Perm()&0077 != 0 || now.Mode().Perm()&0077 != 0 {
		return nil, errWorkspaceLiftState
	}
	var r workspaceLiftRecord
	if agentsuite.Unmarshal(raw, &r) != nil || r.Version != 1 || r.Workspace != c.expected.Workspace || r.Suite != c.expected.Suite || r.Repository != c.expected.Repository || r.Scope != c.expected.Scope || kind == "publication" && r.Placement != nil || kind == "placement" && r.Placement == nil {
		return nil, errWorkspaceLiftState
	}
	c.record = &r
	return c, nil
}
func (c *workspaceLiftCompletion) commit(ctx context.Context, placement *suiteNativePlacement) error {
	if c.output == nil || c.record != nil {
		return errWorkspaceLiftState
	}
	record := c.expected
	record.Placement = placement
	raw, err := agentsuite.Marshal(record)
	if err != nil || len(raw) > 64<<10 {
		return errWorkspaceLiftState
	}
	err = errors.Join(c.output.publishBytes(ctx, raw), c.output.Close())
	c.output = nil
	if err != nil {
		return errors.Join(errWorkspaceLiftState, err)
	}
	c.record = &record
	return nil
}
func (p *PreparedWorkspaceLift) liftEvidence(ctx context.Context, receipt *suitePassingReceipt) (*suitePassingExport, error) {
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	output, err := preflightSuitePassingExport(ctx, filepath.Join(p.store.partition, ".lift-evidence-"+id+".json"), p.review.root, p.store.cache)
	if err != nil {
		return nil, err
	}
	if err := output.publish(ctx, receipt, p.review.suite); err != nil {
		output.Close()
		return nil, err
	}
	return output, nil
}
