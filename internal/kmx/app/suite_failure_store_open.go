package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func openSuiteFailureStore(workspaceRoot string, create bool) (_ *suiteFailureStore, err error) {
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	workspace, err := fileaccess.OpenDirectory(context.Background(), workspaceRoot)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	workspaceIdentity, identityErr := fileaccess.PhysicalIdentity(context.Background(), workspace)
	workspaceCloseErr := workspace.Close()
	if identityErr != nil || workspaceCloseErr != nil {
		return nil, errSuiteFailureStore
	}
	state, err := suiteFailureStateDir(create)
	if !create && (errors.Is(err, fileaccess.ErrMissing) || errors.Is(err, os.ErrNotExist)) {
		return nil, errSuiteFailureMissing
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	base := filepath.Join(state, "eval-failures")
	key := suiteFailureWorkspaceKey(workspaceRoot, workspaceIdentity)
	if suiteReceiptInside(base, workspaceRoot) {
		return nil, errSuiteFailureStore
	}
	return openFailureStore(base, key, create, workspaceFailureCodec{})
}

func openFileFailureStore(filename string, create bool, codec retainedFileFailureCodec) (_ *suiteFailureStore, err error) {
	scope, err := resolveFileFailureScope(filename)
	if err != nil || codec.subject != "" && codec.subject != scope.subject {
		return nil, errSuiteFailureStore
	}
	codec.subject = scope.subject
	return openFileFailureStoreScope(scope, create, codec)
}

func openFileFailureStoreScope(scope fileFailureScope, create bool, codec retainedFileFailureCodec) (_ *suiteFailureStore, err error) {
	if !suitePrefixedDigest(scope.subject) || scope.key != "file-"+scope.subject[len("sha256:"):] || !filepath.IsAbs(scope.filename) {
		return nil, errSuiteFailureStore
	}
	if codec.subject != "" && codec.subject != scope.subject {
		return nil, errSuiteFailureStore
	}
	codec.subject = scope.subject
	state, err := suiteFailureStateDir(create)
	if !create && (errors.Is(err, fileaccess.ErrMissing) || errors.Is(err, os.ErrNotExist)) {
		return nil, errSuiteFailureMissing
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	base := filepath.Join(state, "eval-failures")
	return openFailureStore(base, scope.key, create, codec)
}

func openFileFailureStoreBySubject(subject string) (_ *suiteFailureStore, err error) {
	if !suitePrefixedDigest(subject) {
		return nil, errSuiteFailureStore
	}
	state, err := suiteFailureStateDir(false)
	if errors.Is(err, fileaccess.ErrMissing) || errors.Is(err, os.ErrNotExist) {
		return nil, errSuiteFailureMissing
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	key := "file-" + subject[len("sha256:"):]
	return openFailureStore(filepath.Join(state, "eval-failures"), key, false, retainedFileFailureCodec{subject: subject})
}

func openFailureStore(base, key string, create bool, codec suiteFailureCodec) (_ *suiteFailureStore, err error) {
	path := filepath.Join(base, key)
	if create {
		for _, directory := range []string{base, path} {
			opened, openErr := fileaccess.EnsurePrivateDirectory(context.Background(), directory)
			if openErr != nil {
				return nil, errSuiteFailureStore
			}
			if closeErr := opened.Close(); closeErr != nil {
				return nil, errSuiteFailureStore
			}
		}
	}
	directory, err := fileaccess.OpenPrivateDirectory(context.Background(), path)
	if !create && errors.Is(err, fileaccess.ErrMissing) {
		return nil, errSuiteFailureMissing
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	s := &suiteFailureStore{directory: directory, path: path, codec: codec, publish: fileaccess.PublishPrivateNamed, syncDir: fileaccess.SyncDirectory}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	if create {
		s.lock, err = fileaccess.EnsurePrivateFileAt(context.Background(), directory, "lock")
	} else {
		s.lock, err = fileaccess.OpenPrivateFileAt(context.Background(), directory, "lock")
	}
	if err != nil {
		return nil, errSuiteFailureStore
	}
	s.lockID, err = fileaccess.PhysicalIdentity(context.Background(), s.lock)
	if err != nil {
		return nil, errSuiteFailureStore
	}
	info, statErr := s.lock.Stat()
	links, linksErr := fileaccess.LinkCount(context.Background(), s.lock)
	if statErr != nil || linksErr != nil || links != 1 || info.Size() != 0 {
		return nil, errSuiteFailureStore
	}
	if fileaccess.TryLock(context.Background(), s.lock) != nil {
		return nil, errSuiteFailureBusy
	}
	if s.unchanged() != nil {
		return nil, errSuiteFailureStore
	}
	return s, nil
}

func (s *suiteFailureStore) readEntry(name string, allowOwnPair bool) ([]byte, string, fs.FileInfo, uint64, error) {
	file, err := fileaccess.OpenPrivateFileAt(context.Background(), s.directory, name)
	if err != nil {
		return nil, "", nil, 0, err
	}
	closeFile := func() error {
		if file == nil {
			return nil
		}
		err := file.Close()
		file = nil
		return err
	}
	defer func() { _ = closeFile() }()
	links, err := fileaccess.LinkCount(context.Background(), file)
	if err != nil || links != 1 && (!allowOwnPair || links != 2) {
		return nil, "", nil, 0, errSuiteFailureStore
	}
	identity, err := fileaccess.PhysicalIdentity(context.Background(), file)
	if err != nil {
		return nil, "", nil, 0, errSuiteFailureStore
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxSuiteFailureRecordBytes {
		return nil, "", nil, 0, errSuiteFailureStore
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxSuiteFailureRecordBytes+1))
	after, afterErr := file.Stat()
	closeErr := closeFile()
	if readErr != nil || afterErr != nil || closeErr != nil || len(raw) > maxSuiteFailureRecordBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, "", nil, 0, errSuiteFailureStore
	}
	return raw, identity, after, links, nil
}
