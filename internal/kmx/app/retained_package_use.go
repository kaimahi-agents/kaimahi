package app

import (
	"context"
	"errors"
	"path/filepath"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	godigest "github.com/opencontainers/go-digest"
)

// VerifiedPackageUse is store-constructed and owns exclusive verified access.
// Closing it releases handles, never retained bytes or the caller's store.
type VerifiedPackageUse struct {
	entry    *retainedEntry
	ready    retainedReady
	snapshot *retainedFileSnapshot
	artifact *localartifact.Artifact
	closed   bool
}

func (s *retainedStore) openEntry(ctx context.Context, h string) (entry *retainedEntry, err error) {
	if !retainedHandlePattern.MatchString(h) {
		return nil, errRetainedUnavailable
	}
	if err = s.check(ctx); err != nil {
		return nil, err
	}
	dir, err := fileaccess.OpenDirectoryAt(ctx, s.dir, retainedComponent(h))
	if err != nil {
		return nil, err
	}
	e := &retainedEntry{store: s, handle: h, dir: dir}
	defer func() {
		if err != nil {
			err = errors.Join(err, e.Close())
		}
	}()
	if err = fileaccess.CheckPrivate(ctx, dir, true); err != nil {
		return nil, err
	}
	e.lock, err = fileaccess.OpenPrivateFileAt(ctx, dir, "use.lock")
	if err != nil {
		return nil, err
	}
	locked, err := fileaccess.TryLockOutcome(ctx, e.lock)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, &PackageBusy{}
	}
	links, err := fileaccess.LinkCount(ctx, e.lock)
	if err != nil || links != 1 {
		return nil, errRetainedUnavailable
	}
	e.control, err = s.control(ctx, dir, h)
	if err != nil {
		return nil, err
	}
	if err = e.check(ctx); err != nil {
		return nil, err
	}
	return e, nil
}
func retainedReleased(ctx context.Context, e *retainedEntry) (bool, error) {
	var c retainedControl
	err := retainedRead(ctx, e.dir, "released.json", &c)
	if errors.Is(err, fileaccess.ErrMissing) {
		return false, nil
	}
	if err != nil || c != e.control {
		return false, errRetainedUnavailable
	}
	return true, nil
}
func retainedCompletion(ctx context.Context, e *retainedEntry) (r retainedReady, err error) {
	if err = retainedRead(ctx, e.dir, "ready.json", &r); err != nil {
		return r, err
	}
	p := r.Package
	if r.Domain != retainedDomain || r.Version != 1 || p.Handle != e.handle || p.SourceRevision != r.Source.Revision || p.Runtime != "orka" || p.PackageProfile != localartifact.SourceProfile || p.PackagerVersion != localartifact.SourcePackager || !suitecore.IsValidDigest(p.Digest) || !suitecore.IsValidDigest(p.Fingerprint) || p.Descriptor.Digest.String() != p.Digest {
		return r, errRetainedUnavailable
	}
	return r, nil
}
func (s *retainedStore) OpenVerifiedUse(ctx context.Context, h, r string) (use *VerifiedPackageUse, err error) {
	e, err := s.openEntry(ctx, h)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, e.Close())
		}
	}()
	revoked, err := retainedReleased(ctx, e)
	if err != nil || revoked {
		return nil, errRetainedUnavailable
	}
	ready, err := retainedCompletion(ctx, e)
	if err != nil {
		return nil, err
	}
	if r == "" || r != ready.Package.SourceRevision {
		return nil, errRetainedSourceChanged
	}
	tests := ""
	if ready.Source.Explicit {
		tests = ready.Source.Tests
	}
	snapshot, err := loadRetainedFile(ctx, ready.Source.Agent, tests, r)
	if err != nil {
		return nil, err
	}
	if snapshot.binding != ready.Source {
		return nil, errRetainedSourceChanged
	}
	use = &VerifiedPackageUse{entry: e, ready: ready, snapshot: snapshot}
	if err = use.check(ctx); err != nil {
		return nil, err
	}
	return use, nil
}
func (u *VerifiedPackageUse) check(ctx context.Context) error {
	if u == nil || u.closed || u.entry == nil || u.snapshot == nil {
		return errRetainedUnavailable
	}
	e := u.entry
	if err := e.check(ctx); err != nil {
		return err
	}
	revoked, err := retainedReleased(ctx, e)
	if err != nil || revoked {
		return errRetainedUnavailable
	}
	ready, err := retainedCompletion(ctx, e)
	if err != nil {
		return err
	}
	before, _ := retainedEncode(u.ready)
	after, _ := retainedEncode(ready)
	if string(before) != string(after) {
		return errRetainedUnavailable
	}
	path := filepath.Join(e.store.path, retainedComponent(e.handle))
	if _, err = retainedInventory(ctx, path, e.dir, false); err != nil {
		return err
	}
	artifact, err := localartifact.ReadLayout(ctx, filepath.Join(path, "layout"), ready.Package.Digest)
	if err != nil {
		return err
	}
	f, err := workspace.MappedArtifactFingerprint(ctx, artifact)
	if err != nil || f != ready.Package.Fingerprint {
		return errRetainedUnavailable
	}
	native, err := workspace.LoadArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	p := ready.Package
	if native.MemberID != p.MemberID || native.Name != p.MemberID || native.Model != p.Model || native.Runtime.ID != p.Runtime {
		return errRetainedUnavailable
	}
	source, err := workspace.ReadSources(ctx, artifact)
	if err != nil || godigest.FromBytes(source.Agent).String() != ready.Source.AgentRevision || godigest.FromBytes(source.Tests).String() != ready.Source.TestsRevision {
		return errRetainedUnavailable
	}
	original, _ := suitecore.Marshal(p.Descriptor)
	actual, _ := suitecore.Marshal(artifact.Descriptor())
	if string(original) != string(actual) {
		return errRetainedUnavailable
	}
	if err = errors.Join(e.check(ctx), u.snapshot.recheck(ctx)); err != nil {
		return err
	}
	u.artifact = artifact
	return nil
}
func (u *VerifiedPackageUse) Close() error {
	if u == nil || u.closed {
		return nil
	}
	u.closed = true
	var err error
	if u.entry != nil {
		err = errors.Join(u.entry.check(context.Background()), u.entry.Close())
	}
	u.entry = nil
	u.snapshot = nil
	u.artifact = nil
	return err
}
