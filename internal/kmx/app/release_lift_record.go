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

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"golang.org/x/sys/unix"
)

const (
	maxReleaseLiftAttempts    = 256
	maxReleaseLiftStoreBytes  = 64 << 20
	maxReleaseLiftRecordBytes = maxSuitePassingReceiptBytes + (48 << 10)
)

var (
	errReleaseLiftRecord = errors.New("release Lift record persistence unavailable or uncertain; inspect private KMX state; no replay or cleanup authority")
	releaseLiftRecordID  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Original intent/outcome fields are observations, not evaluation or skip
// authority. New complete Orka creates/updates reserve a separate fifth snapshot
// for explicit ownership plus acknowledged shared completion. Old observations
// are never upgraded. A store-level OS lock bounds capacity, not pair concurrency.
// Layout: release-lifts/<opaque ID>/{intent,1,2,3[,4]}.json.
type releaseLiftRecordWriter struct {
	root                         *os.Root
	lock                         *os.File
	lockInfo                     os.FileInfo
	path, id                     string
	outputs                      []*suitePassingExport
	summary, receipt, annotation json.RawMessage
	next                         int
	updateProof                  *releaseOwnershipProof
	releaseDataDigest            string
}

func releaseLiftRecordError(reason string) error {
	return fmt.Errorf("%w: %s", errReleaseLiftRecord, reason)
}

func preflightReleaseLiftStorage(ctx context.Context) error {
	w, err := openReleaseLiftStorage(ctx)
	if err != nil {
		return err
	}
	return errors.Join(w.probePersistence(ctx), w.Close())
}

// An existing writable lock does not prove the directory permits new records.
// Probe the actual mkdir/write/sync boundary before composed evaluation spend.
func (w *releaseLiftRecordWriter) probePersistence(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := randomHex(16)
	if err != nil || w.root.Mkdir(name, 0700) != nil {
		return releaseLiftRecordError("storage creation probe failed")
	}
	owned, err := w.root.Lstat(name)
	if err != nil {
		return releaseLiftRecordError("storage probe identity uncertain")
	}
	var child *os.Root
	var fileInfo os.FileInfo
	defer func() {
		cleanupFailed := false
		if child != nil {
			if fileInfo != nil {
				current, e := child.Lstat("probe")
				if e != nil || !os.SameFile(current, fileInfo) || child.Remove("probe") != nil {
					cleanupFailed = true
				}
			}
			if child.Close() != nil {
				cleanupFailed = true
			}
		}
		current, e := w.root.Lstat(name)
		if e != nil || !os.SameFile(current, owned) || w.root.Remove(name) != nil {
			cleanupFailed = true
		}
		dir, e := w.root.Open(".")
		if e != nil {
			cleanupFailed = true
		} else if errors.Join(dir.Sync(), dir.Close()) != nil {
			cleanupFailed = true
		}
		if cleanupFailed {
			err = errors.Join(err, releaseLiftRecordError("storage probe cleanup uncertain"))
		}
	}()
	child, err = w.root.OpenRoot(name)
	if err != nil {
		return releaseLiftRecordError("storage probe open failed")
	}
	opened, openErr := child.Stat(".")
	current, currentErr := w.root.Lstat(name)
	if openErr != nil || currentErr != nil || !os.SameFile(owned, opened) || !os.SameFile(owned, current) {
		return releaseLiftRecordError("storage probe directory changed")
	}
	file, err := child.OpenFile("probe", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return releaseLiftRecordError("storage write probe failed")
	}
	fileInfo, err = file.Stat()
	_, writeErr := file.Write([]byte("{}"))
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(err, writeErr, syncErr, closeErr) != nil {
		return releaseLiftRecordError("storage persistence probe failed")
	}
	dir, err := child.Open(".")
	if err != nil {
		return releaseLiftRecordError("storage directory probe failed")
	}
	if errors.Join(dir.Sync(), dir.Close()) != nil {
		return releaseLiftRecordError("storage directory sync probe failed")
	}
	return ctx.Err()
}

func openReleaseLiftStorage(ctx context.Context) (_ *releaseLiftRecordWriter, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage location")
	}
	base := filepath.Join(state, "release-lifts")
	for i, path := range []string{state, base} {
		if ensureSuitePassDirectory(path, i > 0) != nil {
			return nil, releaseLiftRecordError("unsafe storage directory")
		}
	}
	root, _, _, err := openSessionReferenceDirectory(filepath.Join(base, "lock"))
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	w := &releaseLiftRecordWriter{root: root, path: base}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.Close())
		}
	}()
	dir, err := root.Open(".")
	if err != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	fd, openErr := unix.Openat(int(dir.Fd()), "lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	closeErr := dir.Close()
	if openErr != nil {
		return nil, releaseLiftRecordError("unsafe storage lock")
	}
	w.lock = os.NewFile(uintptr(fd), "lock")
	if closeErr != nil {
		return nil, releaseLiftRecordError("unsafe storage directory")
	}
	w.lockInfo, err = w.lock.Stat()
	if err != nil || !w.lockInfo.Mode().IsRegular() || w.lockInfo.Mode().Perm()&0077 != 0 {
		return nil, releaseLiftRecordError("unsafe storage lock")
	}
	if unix.Flock(int(w.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, releaseLiftRecordError("another Lift record reservation is active")
	}
	if err := w.unchanged(); err != nil {
		return nil, err
	}
	if err := w.capacity(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *releaseLiftRecordWriter) unchanged() error {
	if w == nil || w.root == nil || w.lock == nil {
		return releaseLiftRecordError("closed record writer")
	}
	fresh, _, parent, err := openSessionReferenceDirectory(filepath.Join(w.path, "lock"))
	if err != nil {
		return releaseLiftRecordError("unsafe changed storage directory")
	}
	before, statErr := w.root.Stat(".")
	closeErr := fresh.Close()
	lock, lockErr := w.root.Lstat("lock")
	if statErr != nil || closeErr != nil || lockErr != nil || parent.Mode().Perm()&0077 != 0 || !os.SameFile(before, parent) || !lock.Mode().IsRegular() || lock.Mode().Perm()&0077 != 0 || !os.SameFile(w.lockInfo, lock) {
		return releaseLiftRecordError("unsafe changed storage directory or lock")
	}
	return nil
}

// Only metadata is scanned: there is deliberately no receipt/history reader or
// recovery mechanism. Interrupted attempts consume capacity, including stages.
func (w *releaseLiftRecordWriter) capacity(ctx context.Context) error {
	dir, err := w.root.Open(".")
	if err != nil {
		return releaseLiftRecordError("unsafe storage directory")
	}
	defer dir.Close()
	names, err := dir.Readdirnames(maxReleaseLiftAttempts + 2)
	if err != nil && err != io.EOF {
		return releaseLiftRecordError("unsafe storage scan")
	}
	if len(names) >= maxReleaseLiftAttempts+2 {
		return releaseLiftRecordError("storage capacity exceeded")
	}
	count, total := 0, int64(0)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "lock" {
			continue
		}
		if !releaseLiftRecordID.MatchString(name) {
			return releaseLiftRecordError("unsafe storage entry")
		}
		count++
		info, err := w.root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return releaseLiftRecordError("unsafe attempt directory")
		}
		child, err := w.root.OpenRoot(name)
		if err != nil {
			return releaseLiftRecordError("unsafe attempt directory")
		}
		n, scanErr := releaseLiftRecordDirectoryBytes(ctx, child)
		closeErr := child.Close()
		if scanErr != nil {
			return scanErr
		}
		if closeErr != nil {
			return releaseLiftRecordError("unsafe attempt directory")
		}
		total += n
	}
	if count >= maxReleaseLiftAttempts || total+5*maxReleaseLiftRecordBytes > maxReleaseLiftStoreBytes {
		return releaseLiftRecordError("storage capacity exceeded")
	}
	return w.unchanged()
}

func releaseLiftRecordDirectoryBytes(ctx context.Context, root *os.Root) (int64, error) {
	dir, err := root.Open(".")
	if err != nil {
		return 0, releaseLiftRecordError("unsafe attempt directory")
	}
	defer dir.Close()
	names, err := dir.Readdirnames(9)
	if err != nil && err != io.EOF {
		return 0, releaseLiftRecordError("unsafe attempt scan")
	}
	if len(names) >= 9 {
		return 0, releaseLiftRecordError("storage capacity exceeded")
	}
	var total int64
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return 0, releaseLiftRecordError("unsafe record file")
		}
		if info.Size() > maxReleaseLiftRecordBytes {
			return 0, releaseLiftRecordError("record exceeds capacity")
		}
		total += info.Size()
	}
	return total, nil
}

func preflightReleaseLiftRecord(ctx context.Context, id string, summary ReleaseLiftSummary, receipt []byte, annotation string) (_ *releaseLiftRecordWriter, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !releaseLiftRecordID.MatchString(id) {
		return nil, releaseLiftRecordError("invalid record ID")
	}
	if len(receipt) > maxSuitePassingReceiptBytes || len(annotation) > 8<<10 {
		return nil, releaseLiftRecordError("receipt or annotation exceeds size limit")
	}
	if !json.Valid(receipt) || !json.Valid([]byte(annotation)) || fmt.Sprintf("sha256:%x", sha256.Sum256(receipt)) != summary.EvidenceDigest {
		return nil, releaseLiftRecordError("invalid receipt digest or annotation JSON")
	}
	summaryRaw, err := json.Marshal(summary)
	if err != nil || len(summaryRaw) > 16<<10 || len(summary.Resources) > 2 {
		return nil, releaseLiftRecordError("summary exceeds size limit")
	}
	w, err := openReleaseLiftStorage(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.Close())
		}
	}()
	w.id, w.summary = id, summaryRaw
	w.receipt, w.annotation = append([]byte(nil), receipt...), json.RawMessage(annotation)
	if err := w.root.Mkdir(id, 0700); err != nil {
		if os.IsExist(err) {
			return nil, releaseLiftRecordError("record ID collision")
		}
		return nil, releaseLiftRecordError("unsafe attempt directory creation")
	}
	slots := []string{"intent.json", "1.json", "2.json", "3.json"}
	if summary.Runtime == "orka" && len(summary.Resources) == 2 && ((summary.Resources[0].Action == "create" && summary.Resources[1].Action == "create") || summary.Resources[0].Action == "update") {
		slots = append(slots, "4.json")
	}
	for _, name := range slots {
		path := filepath.Join(w.path, id, name)
		// The publisher needs no workspace exclusion here: this destination is
		// selected solely from validated private state and an opaque fixed ID.
		root, leaf, parent, err := openSessionReferenceDirectory(path)
		if err != nil {
			return nil, releaseLiftRecordError("unsafe attempt directory")
		}
		if parent.Mode().Perm()&0077 != 0 {
			root.Close()
			return nil, releaseLiftRecordError("unsafe attempt directory")
		}
		if _, err := root.Lstat(leaf); !os.IsNotExist(err) {
			root.Close()
			return nil, releaseLiftRecordError("record slot collision")
		}
		dir, err := root.Open(".")
		if err != nil {
			root.Close()
			return nil, releaseLiftRecordError("unsafe attempt directory")
		}
		w.outputs = append(w.outputs, &suitePassingExport{root: root, directory: dir, parent: parent, path: path, name: leaf})
	}
	resources := make([]ReleaseLiftOutcome, 0, len(summary.Resources))
	for _, resource := range summary.Resources {
		resources = append(resources, ReleaseLiftOutcome{ReleaseLiftResource: resource, Outcome: "not-attempted"})
	}
	if err := w.write(ctx, "not-applied", resources); err != nil {
		return nil, err
	}
	// Sync the new attempt's parent before allowing native effects.
	dir, err := w.root.Open(".")
	if err != nil {
		return nil, releaseLiftRecordError("intent durability uncertain")
	}
	if errors.Join(dir.Sync(), dir.Close()) != nil {
		return nil, releaseLiftRecordError("intent durability uncertain")
	}
	return w, nil
}

func (w *releaseLiftRecordWriter) checkpoint(ctx context.Context, result ReleaseLiftResult) error {
	// Select only observations, never opaque placement authority or future result
	// fields. Summary, receipt and annotation remain the frozen intent snapshot.
	return w.write(ctx, result.State, result.Resources)
}

func (w *releaseLiftRecordWriter) write(ctx context.Context, state string, resources []ReleaseLiftOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.unchanged(); err != nil {
		return err
	}
	if w.next >= len(w.outputs) {
		return releaseLiftRecordError("checkpoint capacity exceeded")
	}
	rows, err := json.Marshal(resources)
	if err != nil || len(rows) > 16<<10 || len(resources) > 2 || len(state) > 128 {
		return releaseLiftRecordError("outcome exceeds size limit")
	}
	for i, output := range w.outputs {
		if output.unchanged() != nil {
			return releaseLiftRecordError("unsafe changed attempt directory")
		}
		if i >= w.next {
			if _, err := output.root.Lstat(output.name); !os.IsNotExist(err) {
				return releaseLiftRecordError("record slot collision")
			}
		}
	}
	raw, err := json.Marshal(struct {
		SchemaVersion     int                    `json:"schemaVersion"`
		ID                string                 `json:"id"`
		Summary           json.RawMessage        `json:"summary"`
		Annotation        json.RawMessage        `json:"annotation"`
		State             string                 `json:"state"`
		Resources         json.RawMessage        `json:"resources"`
		UpdateProof       *releaseOwnershipProof `json:"updateProof,omitempty"`
		ReleaseDataDigest string                 `json:"releaseDataDigest,omitempty"`
	}{1, w.id, w.summary, w.annotation, state, rows, w.updateProof, w.releaseDataDigest})
	if err != nil {
		return releaseLiftRecordError("invalid snapshot JSON")
	}
	// Append the already canonical receipt without re-encoding/HTML escaping it:
	// the exact bytes, not merely equivalent JSON, bind EvidenceDigest.
	raw = append(raw[:len(raw)-1], []byte(`,"receipt":`)...)
	raw = append(raw, w.receipt...)
	raw = append(raw, '}')
	if len(raw) > maxReleaseLiftRecordBytes {
		return releaseLiftRecordError("snapshot exceeds size limit")
	}
	output := w.outputs[w.next]
	w.next++ // A failed publication may already be visible; never retry that slot.
	if err := output.publishBytes(ctx, raw); err != nil {
		return errors.Join(errReleaseLiftRecord, err)
	}
	return nil
}

func (w *releaseLiftRecordWriter) Close() error {
	if w == nil {
		return nil
	}
	var err error
	for _, output := range w.outputs {
		err = errors.Join(err, output.Close())
	}
	w.outputs = nil
	if w.lock != nil {
		err = errors.Join(err, w.lock.Close())
		w.lock = nil
	}
	if w.root != nil {
		err = errors.Join(err, w.root.Close())
		w.root = nil
	}
	if err != nil {
		return errors.Join(errReleaseLiftRecord, err)
	}
	return nil
}
