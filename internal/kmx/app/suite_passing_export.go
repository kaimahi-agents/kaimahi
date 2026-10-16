package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuitePassingExport = errors.New("native receipt publication incomplete; output may exist")
var errSuitePassingFile = errors.New("native receipt requires a bounded private regular file and private directory")

// Preflight retains just one anchored destination; no locks or durable state.
// PR5 must call it before runtime selection or native mutation.
type suitePassingExport struct {
	root         *os.Root
	directory    *os.File
	path, name   string
	parent       os.FileInfo
	linked       bool
	publicParent bool
	beforeLink   func() error
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
	return &suitePassingExport{root: root, directory: directory, path: destination, name: name, parent: parent, publicParent: publicParent}, nil
}

func (e *suitePassingExport) Close() error {
	if errors.Join(e.directory.Close(), e.root.Close()) != nil {
		return errSuitePassingExport
	}
	return nil
}

func (e *suitePassingExport) unchanged() error {
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
	if e.directory.Sync() != nil {
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
	if err != nil {
		return nil, errSuitePassingFile
	}
	if !os.SameFile(before, info) {
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
	return err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
