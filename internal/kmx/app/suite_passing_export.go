package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuitePassingExport = errors.New("native receipt publication incomplete; output may exist")
var errSuitePassingFile = errors.New("native receipt requires a bounded private regular file and private directory")

// Preflight retains just one anchored destination; no locks or durable state.
// PR5 must call it before runtime selection or native mutation.
type suitePassingExport struct {
	// Public evidence and release records retain their existing safe-public-parent
	// os.Root publisher. Private receipts use the checked cross-platform handle.
	root               *os.Root
	directory          io.Closer
	legacyDirectory    *os.File
	privateDirectory   *fileaccess.Directory
	path, name         string
	parent             os.FileInfo
	parentIdentity     string
	linked             bool
	visibilityKnown    bool
	visibilityReported bool
	namedPathCurrent   bool
	publicParent       bool
	beforeLink         func() error
	publishPrivate     func(context.Context, *fileaccess.Directory, string, []byte) (fileaccess.PublicationResult, error)
}

func preflightSuitePassingExport(ctx context.Context, destination, workspaceRoot, cacheRoot string) (*suitePassingExport, error) {
	return preflightSuitePassingExportMode(ctx, destination, workspaceRoot, cacheRoot, false)
}

// Public exports permit safe 0755 parents; private defaults remain unchanged.
func preflightSuitePublicExport(ctx context.Context, destination, workspaceRoot, cacheRoot string) (*suitePassingExport, error) {
	return preflightSuitePassingExportMode(ctx, destination, workspaceRoot, cacheRoot, true)
}

func preflightSuitePassingExportMode(ctx context.Context, destination, workspaceRoot, cacheRoot string, publicParent bool) (*suitePassingExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scaffold.RefuseKeyShapes(destination) != nil || suiteReceiptInside(destination, workspaceRoot) || suiteReceiptInside(destination, cacheRoot) {
		return nil, errSuitePassingFile
	}
	if !publicParent && checkedPrivateSuiteExport {
		cacheInside := false
		if _, cacheErr := os.Stat(cacheRoot); cacheErr == nil {
			cacheInside = suiteReceiptPhysicallyInside(ctx, destination, cacheRoot)
		} else if !os.IsNotExist(cacheErr) {
			return nil, errSuitePassingFile
		}
		if suiteReceiptPhysicallyInside(ctx, destination, workspaceRoot) || cacheInside {
			return nil, errSuitePassingFile
		}
		return preflightSuitePrivateExport(ctx, destination)
	}
	root, name, parent, err := openSessionReferenceDirectory(destination)
	if err != nil {
		return nil, errSuitePassingFile
	}
	if !publicParent && parent.Mode().Perm()&0077 != 0 {
		root.Close()
		return nil, errSuitePassingFile
	}
	if _, err := root.Lstat(name); !os.IsNotExist(err) {
		root.Close()
		return nil, errSuitePassingFile
	}
	directory, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, errSuitePassingFile
	}
	return &suitePassingExport{root: root, directory: directory, legacyDirectory: directory, path: destination, name: name, parent: parent, namedPathCurrent: true, publicParent: publicParent}, nil
}

func preflightSuitePrivateExport(ctx context.Context, destination string) (*suitePassingExport, error) {
	absolute, err := filepath.Abs(destination)
	if err != nil || filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return nil, errSuitePassingFile
	}
	directory, err := fileaccess.OpenPrivateDirectory(ctx, filepath.Dir(absolute))
	if err != nil {
		return nil, errSuitePassingFile
	}
	closeOnError := func() {
		_ = directory.Close()
	}
	identity, err := fileaccess.PhysicalIdentity(ctx, directory)
	if err != nil {
		closeOnError()
		return nil, errSuitePassingFile
	}
	output := &suitePassingExport{
		directory:        directory,
		privateDirectory: directory,
		path:             absolute,
		name:             filepath.Base(absolute),
		parentIdentity:   identity,
		visibilityKnown:  true,
		namedPathCurrent: true,
		publishPrivate:   fileaccess.PublishPrivate,
	}
	if err := output.absent(ctx); err != nil {
		closeOnError()
		return nil, err
	}
	return output, nil
}

func (e *suitePassingExport) Close() error {
	if e == nil || e.directory == nil {
		return errSuitePassingExport
	}
	err := e.directory.Close()
	e.directory = nil
	if e.root != nil {
		err = errors.Join(err, e.root.Close())
		e.root = nil
	}
	if err != nil {
		return errSuitePassingExport
	}
	return nil
}

func (e *suitePassingExport) unchanged() error {
	if e == nil {
		return errSuitePassingExport
	}
	if e.privateDirectory != nil {
		ctx := context.Background()
		if e.directory == nil {
			return errSuitePassingExport
		}
		held, heldErr := fileaccess.PhysicalIdentity(ctx, e.privateDirectory)
		fresh, err := fileaccess.OpenPrivateDirectory(ctx, filepath.Dir(e.path))
		if err != nil {
			return errSuitePassingExport
		}
		now, identityErr := fileaccess.PhysicalIdentity(ctx, fresh)
		closeErr := fresh.Close()
		if heldErr != nil || identityErr != nil || closeErr != nil || held != e.parentIdentity || now != e.parentIdentity {
			return errSuitePassingExport
		}
		return nil
	}
	root, _, parent, err := openSessionReferenceDirectory(e.path)
	if err != nil {
		return errSuitePassingExport
	}
	closeErr := root.Close()
	if closeErr != nil || !e.publicParent && parent.Mode().Perm()&0077 != 0 || !os.SameFile(e.parent, parent) {
		return errSuitePassingExport
	}
	return nil
}

func (e *suitePassingExport) absent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.unchanged(); err != nil {
		return err
	}
	if e.privateDirectory != nil {
		file, err := fileaccess.OpenPrivateFileAt(ctx, e.privateDirectory, e.name)
		if errors.Is(err, fileaccess.ErrMissing) {
			return nil
		}
		if err == nil {
			_ = file.Close()
		}
		return errSuitePassingExport
	}
	if _, err := e.root.Lstat(e.name); !os.IsNotExist(err) {
		return errSuitePassingExport
	}
	return nil
}

// Full offline coherence is required at this API, not only at construction.
// Errors after the exclusive link may leave the published output in place.
func (e *suitePassingExport) publish(ctx context.Context, r *suitePassingReceipt, suite *workspace.NativeSuite) (err error) {
	if err := suite.RequireUnprofiled(); err != nil {
		return err
	}
	raw, err := canonicalSuitePassingReceipt(ctx, r, suite)
	if err != nil {
		return err
	}
	return e.publishBytes(ctx, raw)
}

// Callers validate the complete document before using this exclusive publisher.
// linked records the visibility boundary even when later persistence checks fail.
func (e *suitePassingExport) publishBytes(ctx context.Context, raw []byte) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.unchanged(); err != nil {
		return err
	}
	defer func() {
		if e.linked {
			if currentErr := e.unchanged(); currentErr != nil {
				e.namedPathCurrent = false
				err = errors.Join(err, errSuitePassingExport)
			}
		}
	}()
	if e.privateDirectory != nil {
		if e.beforeLink != nil {
			if err := e.beforeLink(); err != nil {
				return err
			}
			if err := e.unchanged(); err != nil {
				return err
			}
		}
		if e.publishPrivate == nil {
			return errSuitePassingExport
		}
		result, publishErr := e.publishPrivate(ctx, e.privateDirectory, e.name, raw)
		e.linked = result.Visible
		e.visibilityKnown = result.VisibilityKnown
		e.visibilityReported = true
		if publishErr == nil {
			return nil
		}
		if result.Visible || !result.VisibilityKnown {
			return errors.Join(errSuitePassingExport, publishErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return errSuitePassingExport
	}
	suffix, err := randomHex(16)
	if err != nil {
		return errSuitePassingExport
	}
	stage := ".native-receipt-" + suffix
	file, err := e.root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errSuitePassingExport
	}
	stageInfo, statErr := file.Stat()
	removeOwned := func() error {
		current, currentErr := e.root.Lstat(stage)
		if statErr != nil || currentErr != nil || !os.SameFile(stageInfo, current) {
			return errSuitePassingExport
		}
		return e.root.Remove(stage)
	}
	removed := false
	defer func() {
		if !removed && removeOwned() != nil {
			err = errors.Join(err, errSuitePassingExport)
		}
	}()
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return errSuitePassingExport
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.unchanged(); err != nil {
		return err
	}
	current, currentErr := e.root.Lstat(stage)
	if statErr != nil || currentErr != nil || !os.SameFile(stageInfo, current) {
		return errSuitePassingExport
	}
	if e.beforeLink != nil {
		if err := e.beforeLink(); err != nil {
			return err
		}
		if err := e.unchanged(); err != nil {
			return err
		}
	}
	if e.root.Link(stage, e.name) != nil {
		return errSuitePassingExport
	}
	e.linked = true
	if err := e.unchanged(); err != nil {
		return err
	}
	if removeOwned() != nil {
		return errSuitePassingExport
	}
	removed = true
	if e.legacyDirectory == nil || e.legacyDirectory.Sync() != nil {
		return errSuitePassingExport
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), errSuitePassingExport)
	}
	if err := e.unchanged(); err != nil {
		return err
	}
	return nil
}

func readSuitePassingReceipt(ctx context.Context, filename string) (receipt *suitePassingReceipt, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scaffold.RefuseKeyShapes(filename) != nil {
		return nil, errSuitePassingFile
	}
	if !checkedPrivateSuiteExport {
		return readSuitePassingReceiptLegacy(ctx, filename)
	}
	return readSuitePassingReceiptChecked(ctx, filename)
}

func readSuitePassingReceiptChecked(ctx context.Context, filename string) (receipt *suitePassingReceipt, err error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, errSuitePassingFile
	}
	parentPath, name := filepath.Dir(absolute), filepath.Base(absolute)
	directory, err := fileaccess.OpenPrivateDirectory(ctx, parentPath)
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer func() {
		if directory.Close() != nil {
			err = errors.Join(err, errSuitePassingFile)
		}
	}()
	parentIdentity, err := fileaccess.PhysicalIdentity(ctx, directory)
	if err != nil {
		return nil, errSuitePassingFile
	}
	file, err := fileaccess.OpenPrivateFileAt(ctx, directory, name)
	if err != nil {
		return nil, errSuitePassingFile
	}
	before, statErr := file.Stat()
	identity, identityErr := fileaccess.PhysicalIdentity(ctx, file)
	if statErr != nil || identityErr != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxSuitePassingReceiptBytes {
		_ = file.Close()
		return nil, errSuitePassingFile
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxSuitePassingReceiptBytes+1))
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || afterErr != nil || closeErr != nil || len(raw) > maxSuitePassingReceiptBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errSuitePassingFile
	}
	freshParent, openErr := fileaccess.OpenPrivateDirectory(ctx, parentPath)
	if openErr != nil {
		return nil, errSuitePassingFile
	}
	nowParent, parentErr := fileaccess.PhysicalIdentity(ctx, freshParent)
	current, currentErr := fileaccess.OpenPrivateFileAt(ctx, freshParent, name)
	var currentIdentity string
	var currentInfo os.FileInfo
	if currentErr == nil {
		currentIdentity, currentErr = fileaccess.PhysicalIdentity(ctx, current)
		if currentErr == nil {
			currentInfo, currentErr = current.Stat()
		}
		currentErr = errors.Join(currentErr, current.Close())
	}
	freshCloseErr := freshParent.Close()
	if parentErr != nil || currentErr != nil || freshCloseErr != nil || nowParent != parentIdentity || currentIdentity != identity || currentInfo == nil || currentInfo.Size() != before.Size() || currentInfo.Mode() != before.Mode() || !currentInfo.ModTime().Equal(before.ModTime()) {
		return nil, errSuitePassingFile
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseSuitePassingReceipt(raw)
}

func readSuitePassingReceiptLegacy(ctx context.Context, filename string) (receipt *suitePassingReceipt, err error) {
	root, name, parent, err := openSessionReferenceDirectory(filename)
	if err != nil {
		return nil, errSuitePassingFile
	}
	if parent.Mode().Perm()&0077 != 0 {
		root.Close()
		return nil, errSuitePassingFile
	}
	defer func() {
		if root.Close() != nil {
			err = errors.Join(err, errSuitePassingFile)
		}
	}()
	directory, err := root.Open(".")
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer func() {
		if directory.Close() != nil {
			err = errors.Join(err, errSuitePassingFile)
		}
	}()
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errSuitePassingFile
	}
	file, err := openKagentFile(directory, name)
	if err != nil {
		return nil, errSuitePassingFile
	}
	defer func() {
		if file.Close() != nil {
			err = errors.Join(err, errSuitePassingFile)
		}
	}()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		return nil, errSuitePassingFile
	}
	raw, err := readKagentOpenedFile(file, info, true)
	if err != nil {
		return nil, errSuitePassingFile
	}
	current, currentErr := root.Lstat(name)
	again, _, now, openErr := openSessionReferenceDirectory(filename)
	if openErr != nil {
		return nil, errSuitePassingFile
	}
	closeErr := again.Close()
	if currentErr != nil || closeErr != nil || now.Mode().Perm()&0077 != 0 || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !os.SameFile(info, current) || !os.SameFile(parent, now) {
		return nil, errSuitePassingFile
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseSuitePassingReceipt(raw)
}

func suiteReceiptInside(filename, tree string) bool {
	absolute, err := filepath.Abs(filename)
	if err != nil || tree == "" {
		return true
	}
	root, err := filepath.Abs(tree)
	if err != nil {
		return true
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		rootVolume, absoluteVolume := filepath.VolumeName(root), filepath.VolumeName(absolute)
		if rootVolume != "" && absoluteVolume != "" && !strings.EqualFold(rootVolume, absoluteVolume) {
			return false
		}
		return true
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func suiteReceiptPhysicallyInside(ctx context.Context, filename, tree string) bool {
	if tree == "" {
		return true
	}
	inside, err := fileaccess.PhysicallyContains(ctx, tree, filename)
	return err != nil || inside
}
