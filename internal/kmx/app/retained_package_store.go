package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const retainedEntryBytes = 32 << 20

// A fresh H has no diagnostics: graph <=9MiB, two layout copies <=11MiB
// each, and 64KiB of control/publication overhead fit before any bulk write.
const retainedConstructionBudget = (31 << 20) + (64 << 10)
const retainedMetadataBytes = 16 << 10
const retainedDomain = "kmx.retained-package/v1"

var retainedHandlePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var errRetainedCapacity = errors.New("retained_capacity")
var errRetainedUnavailable = errors.New("retained package state is unavailable")

type PackageBusy struct{}

func (*PackageBusy) Error() string { return "package_busy" }

type FamilyBusy struct{}

func (*FamilyBusy) Error() string { return "family_busy" }

type RetainedPrepareOptions struct{ File, TestsFile, ExpectedRevision string }
type RetainedPackageSummary struct {
	Handle          string             `json:"handle"`
	SourceRevision  string             `json:"sourceRevision"`
	Digest          string             `json:"digest"`
	Descriptor      ocispec.Descriptor `json:"descriptor"`
	PackagerVersion string             `json:"packagerVersion"`
	PackageProfile  string             `json:"packageProfile"`
	MemberID        string             `json:"memberId"`
	Runtime         string             `json:"runtime"`
	Model           string             `json:"model"`
	Fingerprint     string             `json:"inputFingerprint"`
}
type retainedControl struct {
	Domain  string `json:"domain"`
	Version int    `json:"version"`
	Handle  string `json:"handle"`
	Root    string `json:"root"`
	Entry   string `json:"entry"`
}
type retainedReady struct {
	Domain  string                 `json:"domain"`
	Version int                    `json:"version"`
	Package RetainedPackageSummary `json:"package"`
	Source  retainedFileBinding    `json:"source"`
}
type retainedStore struct {
	path             string
	state, base, dir *fileaccess.Directory
	publish          func(context.Context, *fileaccess.Directory, string, []byte, string) (fileaccess.PublicationResult, error)
}
type retainedEntry struct {
	store   *retainedStore
	handle  string
	dir     *fileaccess.Directory
	lock    *fileaccess.PrivateFile
	control retainedControl
}

func retainedComponent(handle string) string { return "pkg-" + handle }

func (s *retainedStore) Close() error {
	if s == nil {
		return nil
	}
	var err error
	for _, d := range []*fileaccess.Directory{s.dir, s.base, s.state} {
		if d != nil {
			err = errors.Join(err, d.Close())
		}
	}
	s.dir = nil
	s.base = nil
	s.state = nil
	return err
}
func openRetainedStore(ctx context.Context, create bool) (*retainedStore, error) {
	return openRetainedStoreWithSync(ctx, create, fileaccess.SyncDirectory)
}
func openRetainedStoreWithSync(ctx context.Context, create bool, syncDirectory func(context.Context, *fileaccess.Directory) error) (s *retainedStore, err error) {
	if err = retainedPlatform(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var root string
	if create {
		root, err = config.PrepareStateDir()
	} else {
		root, err = config.ExistingStateDir()
	}
	if err != nil {
		return nil, err
	}
	s = &retainedStore{path: filepath.Join(root, "retained-packages", "v1"), publish: fileaccess.PublishPrivateNamed}
	owned := s
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	s.state, err = fileaccess.OpenPrivateDirectory(ctx, root)
	if err != nil {
		return nil, err
	}
	child := fileaccess.OpenDirectoryAt
	if create {
		child = fileaccess.EnsurePrivateDirectoryAt
	}
	s.base, err = child(ctx, s.state, "retained-packages")
	if err != nil {
		return nil, err
	}
	s.dir, err = child(ctx, s.base, "v1")
	if err != nil {
		return nil, err
	}
	if err = fileaccess.CheckPrivate(ctx, s.base, true); err != nil {
		return nil, err
	}
	if err = fileaccess.CheckPrivate(ctx, s.dir, true); err != nil {
		return nil, err
	}
	if create {
		lock, e := fileaccess.EnsurePrivateFileAt(ctx, s.dir, "family.lock")
		if e != nil {
			return nil, e
		}
		if err = errors.Join(fileaccess.SyncFile(ctx, lock), lock.Close(), syncDirectory(ctx, s.dir), syncDirectory(ctx, s.base), syncDirectory(ctx, s.state)); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func retainedSame(ctx context.Context, a, b fileaccess.Object) error {
	left, err := fileaccess.PhysicalIdentity(ctx, a)
	if err != nil {
		return err
	}
	right, err := fileaccess.PhysicalIdentity(ctx, b)
	if err != nil || left != right {
		return errRetainedUnavailable
	}
	return fileaccess.CheckPrivate(ctx, b, false)
}
func (s *retainedStore) check(ctx context.Context) error {
	if s == nil || s.dir == nil {
		return errRetainedUnavailable
	}
	fresh, err := fileaccess.OpenPrivateDirectory(ctx, filepath.Dir(filepath.Dir(s.path)))
	if err != nil {
		return err
	}
	defer fresh.Close()
	if err = retainedSame(ctx, s.state, fresh); err != nil {
		return err
	}
	base, err := fileaccess.OpenDirectoryAt(ctx, fresh, "retained-packages")
	if err != nil {
		return err
	}
	defer base.Close()
	if err = retainedSame(ctx, s.base, base); err != nil {
		return err
	}
	dir, err := fileaccess.OpenDirectoryAt(ctx, base, "v1")
	if err != nil {
		return err
	}
	return errors.Join(retainedSame(ctx, s.dir, dir), dir.Close())
}
func (s *retainedStore) familyLock(ctx context.Context) (lock *fileaccess.PrivateFile, err error) {
	if err = s.check(ctx); err != nil {
		return nil, err
	}
	lock, err = fileaccess.OpenPrivateFileAt(ctx, s.dir, "family.lock")
	if err != nil {
		return nil, err
	}
	owned := lock
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		var acquired bool
		acquired, err = fileaccess.TryLockOutcome(ctx, lock)
		if err != nil {
			return nil, err
		}
		if acquired {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, &FamilyBusy{}
		case <-timer.C:
		}
	}
	current, e := fileaccess.OpenPrivateFileAt(ctx, s.dir, "family.lock")
	if e != nil {
		return nil, e
	}
	if err = errors.Join(retainedSame(ctx, lock, current), current.Close(), s.check(ctx)); err != nil {
		return nil, err
	}
	return lock, nil
}
func (s *retainedStore) handles(ctx context.Context) ([]string, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	fresh, err := fileaccess.OpenDirectoryAt(ctx, s.base, "v1")
	if err != nil {
		return nil, err
	}
	entries, readErr := fresh.ReadDir(258)
	err = errors.Join(readErr, retainedSame(ctx, s.dir, fresh), fresh.Close())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == "family.lock" && !entry.IsDir() {
			continue
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "pkg-") || !retainedHandlePattern.MatchString(strings.TrimPrefix(entry.Name(), "pkg-")) {
			return nil, errRetainedUnavailable
		}
		names = append(names, strings.TrimPrefix(entry.Name(), "pkg-"))
	}
	if len(names) > 256 {
		return nil, errRetainedCapacity
	}
	slices.Sort(names)
	return names, nil
}
func (e *retainedEntry) Close() error {
	if e == nil {
		return nil
	}
	var err error
	if e.lock != nil {
		err = e.lock.Close()
	}
	if e.dir != nil {
		err = errors.Join(err, e.dir.Close())
	}
	e.lock = nil
	e.dir = nil
	return err
}
func (e *retainedEntry) check(ctx context.Context) error {
	if e == nil || e.dir == nil {
		return errRetainedUnavailable
	}
	if err := e.store.check(ctx); err != nil {
		return err
	}
	current, err := fileaccess.OpenDirectoryAt(ctx, e.store.dir, retainedComponent(e.handle))
	if err != nil {
		return err
	}
	if err = errors.Join(retainedSame(ctx, e.dir, current), current.Close()); err != nil {
		return err
	}
	c, controlErr := e.store.control(ctx, e.dir, e.handle)
	if controlErr != nil || c != e.control {
		return errRetainedUnavailable
	}
	lock, err := fileaccess.OpenPrivateFileAt(ctx, e.dir, "use.lock")
	if err != nil {
		return err
	}
	return errors.Join(retainedSame(ctx, e.lock, lock), lock.Close())
}
func (s *retainedStore) allocate(ctx context.Context) (e *retainedEntry, err error) {
	family, err := s.familyLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, family.Close()) }()
	names, err := s.handles(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) >= 256 || retainedConstructionBudget > retainedEntryBytes {
		return nil, errRetainedCapacity
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, errRetainedUnavailable
	}
	e = &retainedEntry{store: s, handle: hex.EncodeToString(random[:])}
	e.dir, err = fileaccess.CreatePrivateDirectoryAt(ctx, s.dir, retainedComponent(e.handle))
	if err != nil {
		return nil, err
	}
	owned := e
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	root, err := fileaccess.PhysicalIdentity(ctx, s.dir)
	if err != nil {
		return nil, err
	}
	entry, err := fileaccess.PhysicalIdentity(ctx, e.dir)
	if err != nil {
		return nil, err
	}
	e.control = retainedControl{Domain: retainedDomain, Version: 1, Handle: e.handle, Root: root, Entry: entry}
	// The family lock excludes allocation/recovery until durable ownership is
	// visible. No use.lock-only unmarked crash state is produced.
	if _, err = s.publishMetadata(ctx, e.dir, "control.json", e.control); err != nil {
		return nil, err
	}
	if err = errors.Join(fileaccess.SyncDirectory(ctx, e.dir), fileaccess.SyncDirectory(ctx, s.dir)); err != nil {
		return nil, err
	}
	e.lock, err = fileaccess.CreatePrivateFileAt(ctx, e.dir, "use.lock")
	if err != nil {
		return nil, err
	}
	locked, err := fileaccess.TryLockOutcome(ctx, e.lock)
	if err != nil || !locked {
		return nil, errRetainedUnavailable
	}
	if err = errors.Join(fileaccess.SyncFile(ctx, e.lock), fileaccess.SyncDirectory(ctx, e.dir)); err != nil {
		return nil, err
	}
	return e, nil
}

func (a *App) PrepareRetainedFile(ctx context.Context, opt RetainedPrepareOptions) (RetainedPackageSummary, error) {
	return prepareRetainedFile(ctx, opt, nil)
}
func prepareRetainedFile(ctx context.Context, opt RetainedPrepareOptions, fault func(string) error) (summary RetainedPackageSummary, err error) {
	if err = retainedPlatform(); err != nil {
		return summary, err
	}
	snapshot, err := loadRetainedFile(ctx, opt.File, opt.TestsFile, opt.ExpectedRevision)
	if err != nil {
		return summary, err
	}
	s, err := openRetainedStore(ctx, true)
	if err != nil {
		return summary, err
	}
	defer func() {
		err = errors.Join(err, s.Close())
		if err != nil {
			summary = RetainedPackageSummary{}
		}
	}()
	e, err := s.allocate(ctx)
	if err != nil {
		return summary, err
	}
	defer func() {
		err = errors.Join(err, e.Close())
		if err != nil {
			summary = RetainedPackageSummary{}
		}
	}()
	point := func(name string) error {
		if fault != nil && fault(name) != nil {
			return errRetainedUnavailable
		}
		return ctx.Err()
	}
	if err = point("owned"); err != nil {
		return summary, err
	}
	build, err := fileaccess.CreatePrivateDirectoryAt(ctx, e.dir, "build")
	if err != nil {
		return summary, err
	}
	if err = build.Close(); err != nil {
		return summary, err
	}
	if err = e.check(ctx); err != nil {
		return summary, err
	}
	path := filepath.Join(s.path, retainedComponent(e.handle))
	compiled, err := workspace.CompileMapped(ctx, snapshot.input, filepath.Join(path, "build"))
	if err != nil {
		return summary, fmt.Errorf("compile retained package: %w", err)
	}
	defer func() { err = errors.Join(err, compiled.Close()) }()
	if err = e.check(ctx); err != nil {
		return summary, err
	}
	descriptor := compiled.Descriptor()
	fingerprint := compiled.Fingerprint()
	if _, err = compiled.CopyToLayout(ctx, filepath.Join(path, "layout")); err != nil {
		return summary, fmt.Errorf("copy retained package: %w", err)
	}
	if err = errors.Join(e.check(ctx), point("copied")); err != nil {
		return summary, err
	}
	if err = compiled.Close(); err != nil {
		return summary, fmt.Errorf("close retained compiler: %w", err)
	}
	if err = point("closed"); err != nil {
		return summary, err
	}
	if err = retainedFinalize(ctx, path, e.dir); err != nil {
		return summary, err
	}
	if err = e.check(ctx); err != nil {
		return summary, err
	}
	artifact, err := localartifact.ReadLayout(ctx, filepath.Join(path, "layout"), descriptor.Digest.String())
	if err != nil {
		return summary, err
	}
	actual, err := workspace.MappedArtifactFingerprint(ctx, artifact)
	if err != nil || actual != fingerprint {
		return summary, errRetainedUnavailable
	}
	if err = errors.Join(e.check(ctx), snapshot.recheck(ctx)); err != nil {
		return summary, err
	}
	if _, err = retainedInventory(ctx, path, e.dir, true); err != nil {
		return summary, err
	}
	if err = point("flushed"); err != nil {
		return summary, err
	}
	summary = RetainedPackageSummary{Handle: e.handle, SourceRevision: opt.ExpectedRevision, Digest: descriptor.Digest.String(), Descriptor: descriptor, PackagerVersion: localartifact.SourcePackager, PackageProfile: localartifact.SourceProfile, MemberID: snapshot.input.MemberID, Runtime: "orka", Model: snapshot.input.Model, Fingerprint: fingerprint}
	ready := retainedReady{Domain: retainedDomain, Version: 1, Package: summary, Source: snapshot.binding}
	if err = errors.Join(e.check(ctx), snapshot.recheck(ctx)); err != nil {
		return RetainedPackageSummary{}, err
	}
	if _, err = s.publishMetadata(ctx, e.dir, "ready.json", ready); err != nil {
		return RetainedPackageSummary{}, err
	}
	if err = point("ready"); err != nil {
		return RetainedPackageSummary{}, err
	}
	return summary, nil
}
