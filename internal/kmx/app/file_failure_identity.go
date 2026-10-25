package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

type fileFailureScope struct {
	subject, key, filename string
}

// A file subject survives replacement of the leaf while remaining bound to the
// checked physical parent. It intentionally contains no recoverable pathname.
func resolveFileFailureScope(filename string) (scope fileFailureScope, err error) {
	absolute, err := filepath.Abs(filename)
	if err != nil || filepath.Clean(absolute) != absolute {
		return fileFailureScope{}, errSuiteFailureStore
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return fileFailureScope{}, errSuiteFailureStore
	}
	subject, key, err := fileFailureSubjectResolved(absolute)
	return fileFailureScope{subject: subject, key: key, filename: absolute}, err
}

func fileFailureSubjectResolved(absolute string) (subject, key string, err error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return "", "", errSuiteFailureStore
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", "", errSuiteFailureStore
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", "", errSuiteFailureStore
	}
	directory, err := fileaccess.OpenDirectory(context.Background(), parent)
	if err != nil {
		return "", "", errSuiteFailureStore
	}
	identity, identityErr := fileaccess.PhysicalIdentity(context.Background(), directory)
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil {
		return "", "", errSuiteFailureStore
	}
	framed := "kmx.agent-file-failure.subject/v1\x00" + identity + "\x00" + fileFailureLeaf(absolute)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(framed)))
	return "sha256:" + digest, "file-" + digest, nil
}

func currentFileFailureScopeRevision(filename string) (fileFailureScope, string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil || filepath.Clean(absolute) != absolute {
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	parent, err := fileaccess.OpenDirectory(context.Background(), filepath.Dir(absolute))
	if err != nil {
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	parentIdentity, identityErr := fileaccess.PhysicalIdentity(context.Background(), parent)
	file, openErr := fileaccess.OpenRegularAt(context.Background(), parent, filepath.Base(absolute))
	if identityErr != nil || openErr != nil {
		_ = parent.Close()
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	before, statErr := file.Stat()
	if statErr != nil || before.Size() > agentfile.MaxFileBytes {
		_ = file.Close()
		_ = parent.Close()
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, agentfile.MaxFileBytes+1))
	after, afterErr := file.Stat()
	closeErr := file.Close()
	parentCloseErr := parent.Close()
	if readErr != nil || afterErr != nil || closeErr != nil || parentCloseErr != nil || len(raw) > agentfile.MaxFileBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fileFailureScope{}, "", errSuiteFailureStore
	}
	leaf := fileFailureLeaf(absolute)
	framed := "kmx.agent-file-failure.subject/v1\x00" + parentIdentity + "\x00" + leaf
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(framed)))
	scope := fileFailureScope{subject: "sha256:" + digest, key: "file-" + digest, filename: absolute}
	return scope, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}
