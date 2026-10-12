package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
)

// A candidate records selection, never evaluation success or qualification.
// Cache paths are derived locally; authored paths/content are not persisted.
type suiteCandidatePin struct {
	SchemaVersion int                `json:"schemaVersion"`
	Domain        string             `json:"domain"`
	Fingerprint   string             `json:"fingerprint"`
	Descriptor    ocispec.Descriptor `json:"descriptor"`
	Name          string             `json:"name"`
}

type suiteCandidateWriter struct {
	root, state, path, key string
	dir, file              *os.File
}

func suiteCandidateError(code string) error { return fmt.Errorf("native suite candidate: %s", code) }

// Exclusive files deliberately survive crashes. No PID heuristic, waiting or
// destructive stale-lock recovery can grant a second writer authority.
func lockSuiteCandidate(root, state string) (*suiteCandidateWriter, error) {
	root, e := suiteAbsolutePath(root)
	if e != nil {
		return nil, suiteCandidateError("workspace unavailable")
	}
	state, e = suiteAbsolutePath(state)
	if e != nil {
		return nil, suiteCandidateError("state unavailable")
	}
	if scaffold.RefuseKeyShapes(root) != nil || scaffold.RefuseKeyShapes(state) != nil || suitePathWithin(root, state) || suitePathWithin(state, root) {
		return nil, suiteCandidateError("invalid private workspace state")
	}
	work, e := openSuiteDirectory(root, false, false)
	if e != nil {
		return nil, suiteCandidateError("workspace unavailable")
	}
	work.Close()
	// Unevaluated publication historically permits a nonprivate state parent.
	// The exclusion directory is always private; pins additionally require the
	// private evaluation-state contract when written or consumed.
	base, e := openSuiteDirectory(state, true, false)
	if e != nil {
		return nil, suiteCandidateError("private state unavailable")
	}
	base.Close()
	path := filepath.Join(state, "suite-candidates")
	dir, e := openSuiteDirectory(path, true, true)
	if e != nil {
		return nil, suiteCandidateError("private state unavailable")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))
	fd, e := unix.Openat(int(dir.Fd()), key+".lock", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		dir.Close()
		return nil, suiteCandidateError("writer exclusion unavailable; inspect active or stale private candidate lock before retrying (no automatic cleanup)")
	}
	file := os.NewFile(uintptr(fd), key+".lock")
	w := &suiteCandidateWriter{root: root, state: state, path: path, key: key, dir: dir, file: file}
	if file.Chmod(0600) != nil || file.Sync() != nil || dir.Sync() != nil {
		w.Close()
		return nil, suiteCandidateError("writer exclusion sync unavailable")
	}
	return w, nil
}
func (w *suiteCandidateWriter) current() bool {
	if w == nil || w.dir == nil || w.file == nil || !suiteDirectoryUnchanged(w.path, w.dir, true) {
		return false
	}
	state, e := openSuiteDirectory(w.state, false, false)
	if e != nil {
		return false
	}
	state.Close()
	f, e := openKagentFile(w.dir, w.key+".lock")
	if e != nil {
		return false
	}
	defer f.Close()
	a, e1 := f.Stat()
	b, e2 := w.file.Stat()
	return e1 == nil && e2 == nil && a.Mode().IsRegular() && a.Mode().Perm() == 0600 && os.SameFile(a, b)
}
func (w *suiteCandidateWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	valid := w.current()
	closeErr := w.file.Close()
	w.file = nil
	defer w.dir.Close()
	if !valid || closeErr != nil {
		return suiteCandidateError("writer exclusion changed; inspect private lock")
	}
	if unix.Unlinkat(int(w.dir.Fd()), w.key+".lock", 0) != nil || w.dir.Sync() != nil {
		return suiteCandidateError("writer exclusion release unavailable; inspect private lock")
	}
	return nil
}
func (w *suiteCandidateWriter) prepared(pin suiteCandidatePin) (*workspace.Prepared, error) {
	if pin.SchemaVersion != 1 || pin.Domain != "suite-candidate" || !spec.IsValidDigest(pin.Fingerprint) || scaffold.ValidateObjectName(pin.Name) != nil || scaffold.RefuseKeyShapes(pin.Name) != nil || pin.Descriptor.Digest.Validate() != nil || pin.Descriptor.Size <= 0 {
		return nil, suiteCandidateError("invalid pin; rerun evaluation after inspection")
	}
	return &workspace.Prepared{Path: filepath.Join(w.state, "suites", strings.TrimPrefix(pin.Fingerprint, "sha256:"), "layout"), Fingerprint: pin.Fingerprint, Descriptor: pin.Descriptor, Name: pin.Name}, nil
}
func (w *suiteCandidateWriter) verify(ctx context.Context, p *workspace.Prepared) error {
	if workspace.Verify(ctx, p) != nil {
		return suiteCandidateError("pinned artifact unavailable; inspect cache and rerun evaluation")
	}
	// Verify the complete descriptor, including its annotations, rather than
	// accepting only OCI's digest/mediaType/size content equality.
	raw, e := readSuitePrivateJSON(filepath.Join(filepath.Dir(p.Path), "prepared.json"))
	if e != nil {
		return suiteCandidateError("prepared record unavailable")
	}
	var record struct {
		Fingerprint string             `json:"fingerprint"`
		Descriptor  ocispec.Descriptor `json:"descriptor"`
		Name        string             `json:"name"`
	}
	if spec.Unmarshal(raw, &record) != nil || record.Fingerprint != p.Fingerprint || record.Name != p.Name || !reflect.DeepEqual(record.Descriptor, p.Descriptor) {
		return suiteCandidateError("pinned descriptor differs from prepared record")
	}
	return nil
}
func (w *suiteCandidateWriter) load(ctx context.Context) (*workspace.Prepared, error) {
	if !w.current() {
		return nil, suiteCandidateError("writer exclusion changed")
	}
	name := w.key + ".json"
	if suiteNoFile(w.dir, name) {
		return nil, nil
	}
	state, e := openSuiteDirectory(w.state, false, true)
	if e != nil {
		return nil, suiteCandidateError("private pin state unavailable")
	}
	state.Close()
	raw, e := readSuitePrivateJSON(filepath.Join(w.path, name))
	if e != nil {
		return nil, suiteCandidateError("private pin unavailable; inspect state")
	}
	var pin suiteCandidatePin
	if !unambiguousSessionsReceiptJSON(raw) || spec.Unmarshal(raw, &pin) != nil || scaffold.RefuseKeyShapes(string(raw)) != nil {
		return nil, suiteCandidateError("invalid private pin; inspect state")
	}
	p, e := w.prepared(pin)
	if e != nil {
		return nil, e
	}
	if e = w.verify(ctx, p); e != nil {
		return nil, e
	}
	return p, nil
}
func (w *suiteCandidateWriter) pin(ctx context.Context, p *workspace.Prepared) (err error) {
	if p == nil || !w.current() {
		return suiteCandidateError("writer exclusion changed")
	}
	state, e := openSuiteDirectory(w.state, false, true)
	if e != nil {
		return suiteCandidateError("private pin state unavailable")
	}
	state.Close()
	pin := suiteCandidatePin{SchemaVersion: 1, Domain: "suite-candidate", Fingerprint: p.Fingerprint, Descriptor: p.Descriptor, Name: p.Name}
	derived, e := w.prepared(pin)
	if e != nil {
		return e
	}
	absolute, e := suiteAbsolutePath(p.Path)
	if e != nil || absolute != derived.Path {
		return suiteCandidateError("candidate must use the private prepared cache")
	}
	if e = w.verify(ctx, p); e != nil {
		return e
	}
	if workspace.CheckCurrent(ctx, w.root, p) != nil {
		return suiteCandidateError("workspace changed; rerun evaluation")
	}
	raw, e := spec.Marshal(pin)
	if e != nil || len(raw) > suiteEvidenceMaxBytes {
		return suiteCandidateError("pin unavailable")
	}
	suffix, e := randomHex(16)
	if e != nil {
		return suiteCandidateError("pin stage unavailable")
	}
	name := ".candidate-" + suffix
	fd, e := unix.Openat(int(w.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return suiteCandidateError("pin stage unavailable")
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	defer func() {
		e := unix.Unlinkat(int(w.dir.Fd()), name, 0)
		if e != nil && !errors.Is(e, os.ErrNotExist) && err == nil {
			err = suiteCandidateError("pin stage cleanup unavailable")
		}
	}()
	n, e := f.Write(raw)
	if e != nil || n != len(raw) || f.Chmod(0600) != nil || f.Sync() != nil || f.Close() != nil {
		return suiteCandidateError("pin write unavailable")
	}
	if ctx.Err() != nil || !w.current() {
		return suiteCandidateError("pin publication unavailable")
	}
	if unix.Renameat(int(w.dir.Fd()), name, int(w.dir.Fd()), w.key+".json") != nil || w.dir.Sync() != nil {
		return suiteCandidateError("pin publication unavailable; inspect private state")
	}
	return nil
}
