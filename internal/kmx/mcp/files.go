// Package mcp adapts local agent-file operations to the stdio MCP boundary.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const maxFileBytes = 256 << 10

// Error contains only fixed classifications, never source or OS error text.
type Error struct{ Code string }

func (e *Error) Error() string {
	switch e.Code {
	case "invalid_root":
		return "invalid_root: select an absolute existing directory with --root"
	case "unsafe_path":
		return "unsafe_path: select a regular agent file beneath the selected root"
	case "invalid_file":
		return "invalid_file: charter validation failed; repair unsupported source manually"
	case "not_found":
		return "not_found: selected source does not exist beneath the root; Create never replaces an existing file"
	case "already_exists":
		return "already_exists: Create never replaces an existing target"
	case "stale_revision":
		return "stale_revision: source changed; review its current revision before editing"
	case "busy":
		return "busy: another editor holds the source lock; try again after it finishes"
	case "lock_unavailable":
		return "lock_unavailable: the OS could not acquire a source lock; no replacement was attempted"
	case "io_failed":
		return "io_failed: could not read or stage the selected source; check access and available space"
	case "publication_failed":
		return "publication_failed: same-directory atomic rename failed; target was not published"
	case "cancelled":
		return "cancelled: operation stopped before publication"
	case "deadline_exceeded":
		return "deadline_exceeded: operation deadline elapsed"
	default:
		return e.Code + ": operation refused"
	}
}
func refusal(code string) error { return &Error{Code: code} }

// File identifies exact saved bytes without returning their content.
type File struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

// Files holds one explicitly selected root. Validate must call the canonical
// parser; injecting it keeps filesystem policy independent of parser ownership.
type Files struct {
	root     *os.Root
	validate func(string, []byte) error
	viewMu   sync.Mutex
	latest   previewObservation
}

func OpenFiles(root string, validate func(string, []byte) error) (*Files, error) {
	if validate == nil {
		return nil, refusal("invalid_options")
	}
	if !filepath.IsAbs(root) {
		return nil, refusal("invalid_root")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, refusal("invalid_root")
	}
	r, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, refusal("invalid_root")
	}
	return &Files{root: r, validate: validate}, nil
}
func (f *Files) Close() error  { return f.root.Close() }
func revision(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

// rootRelative resolves absolute aliases before applying the portable path policy.
// Missing suffixes support Create; an existing dangling link never becomes one.
// File IO still uses the pinned root, not the resolved absolute pathname.
func (f *Files) rootRelative(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return p, nil
	}
	candidate, suffix := filepath.Clean(p), ""
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			break
		}
		parent := filepath.Dir(candidate)
		if !os.IsNotExist(err) || parent == candidate {
			return "", refusal("unsafe_path")
		}
		suffix = filepath.Join(filepath.Base(candidate), suffix)
		candidate = parent
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", refusal("unsafe_path")
	}
	if suffix != "" {
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return "", refusal("unsafe_path")
		}
		resolved = filepath.Join(resolved, suffix)
	}
	// A replaced root pathname must not authorize a different directory from
	// the one retained by the descriptor. This is not a concurrent FS sandbox.
	current, err := os.Stat(f.root.Name())
	if err != nil {
		return "", refusal("unsafe_path")
	}
	opened, err := f.root.Stat(".")
	if err != nil || !os.SameFile(current, opened) {
		return "", refusal("unsafe_path")
	}
	relative, err := filepath.Rel(f.root.Name(), resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", refusal("unsafe_path")
	}
	return filepath.ToSlash(relative), nil
}

var portableID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func safeID(s string) bool {
	if !portableID.MatchString(s) {
		return false
	}
	switch s {
	case "con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return false
	}
	return true
}
func safePath(p string) bool {
	if strings.ContainsAny(p, `\:`) || path.Clean(p) != p {
		return false
	}
	if !strings.Contains(p, "/") {
		return strings.HasSuffix(p, ".agent.md") && safeID(strings.TrimSuffix(p, ".agent.md"))
	}
	parts := strings.Split(p, "/")
	return len(parts) == 4 && parts[0] == ".squad" && parts[1] == "agents" && safeID(parts[2]) && parts[3] == "charter.md"
}
func cancelled(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return refusal("deadline_exceeded")
	}
	if ctx.Err() != nil {
		return refusal("cancelled")
	}
	return nil
}
func (f *Files) valid(p string, b []byte) error {
	if !safePath(p) {
		return refusal("unsafe_path")
	}
	if len(b) == 0 || len(b) > maxFileBytes || f.validate(p, b) != nil {
		return refusal("invalid_file")
	}
	return nil
}
func (f *Files) parent(p string, create bool) (*os.Root, error) {
	dir := path.Dir(p)
	if dir != "." {
		parts := strings.Split(dir, "/")
		for i := range parts {
			name := strings.Join(parts[:i+1], "/")
			info, err := f.root.Lstat(name)
			if os.IsNotExist(err) && create {
				err = f.root.Mkdir(name, 0755)
				if err != nil && !os.IsExist(err) {
					return nil, refusal("io_failed")
				}
				info, err = f.root.Lstat(name)
			}
			if os.IsNotExist(err) {
				return nil, refusal("not_found")
			}
			if err != nil {
				return nil, refusal("io_failed")
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, refusal("unsafe_path")
			}
		}
	}
	r, err := f.root.OpenRoot(filepath.FromSlash(dir))
	if err != nil {
		return nil, refusal("unsafe_path")
	}
	return r, nil
}
func readTarget(parent *os.Root, name string) (*os.File, []byte, os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil, nil, refusal("not_found")
	}
	if err != nil {
		return nil, nil, nil, refusal("io_failed")
	}
	if !before.Mode().IsRegular() || before.Size() > maxFileBytes {
		return nil, nil, nil, refusal("unsafe_path")
	}
	file, err := openTarget(parent, name)
	if err != nil {
		return nil, nil, nil, refusal("unsafe_path")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || !singleLink(file) {
		file.Close()
		return nil, nil, nil, refusal("unsafe_path")
	}
	b, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(b) > maxFileBytes {
		file.Close()
		return nil, nil, nil, refusal("io_failed")
	}
	return file, b, info, nil
}
func stage(parent *os.Root, b []byte, mode os.FileMode, preserveMode bool) (string, error) {
	name := ".kmx-mcp-" + rand.Text() + ".tmp"
	createMode := mode.Perm()
	if preserveMode {
		createMode = 0600
	}
	file, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, createMode)
	if err != nil {
		return "", refusal("io_failed")
	}
	_, writeErr := file.Write(b)
	// Create honors umask; Edit preserves the existing file's mode bits.
	var modeErr error
	if preserveMode {
		modeErr = file.Chmod(mode.Perm())
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if errors.Join(writeErr, modeErr, syncErr, closeErr) != nil {
		parent.Remove(name)
		return "", refusal("io_failed")
	}
	return name, nil
}
func (f *Files) Create(ctx context.Context, p string, b []byte) (File, error) {
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	p, err := f.rootRelative(p)
	if err != nil {
		return File{}, err
	}
	if err := f.valid(p, b); err != nil {
		return File{}, err
	}
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	parent, err := f.parent(p, true)
	if err != nil {
		return File{}, err
	}
	defer parent.Close()
	name, err := stage(parent, b, 0644, false)
	if err != nil {
		return File{}, err
	}
	defer parent.Remove(name)
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	if err := renameExclusive(parent, name, path.Base(p)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return File{}, refusal("already_exists")
		}
		return File{}, refusal("publication_failed")
	}
	return File{Path: p, Revision: revision(b)}, nil
}
func (f *Files) Edit(ctx context.Context, p, expected string, b []byte) (File, error) {
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	p, err := f.rootRelative(p)
	if err != nil {
		return File{}, err
	}
	if err := f.valid(p, b); err != nil {
		return File{}, err
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return File{}, err
	}
	defer parent.Close()
	target, old, info, err := readTarget(parent, path.Base(p))
	if err != nil {
		return File{}, err
	}
	defer target.Close()
	if revision(old) != expected {
		return File{}, refusal("stale_revision")
	}
	if f.validate(p, old) != nil {
		return File{}, refusal("invalid_file")
	}
	name, err := stage(parent, b, info.Mode(), true)
	if err != nil {
		return File{}, err
	}
	defer parent.Remove(name)
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	unlock, err := lockTarget(target)
	if err != nil {
		return File{}, lockFailure(err)
	}
	defer unlock()
	// Locks follow inodes across replacement. A waiter on the replaced inode
	// must refuse rather than treating that obsolete lock as current authority.
	current, err := parent.Lstat(path.Base(p))
	if err != nil || !os.SameFile(info, current) || current.Mode() != info.Mode() || !singleLink(target) {
		return File{}, refusal("stale_revision")
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return File{}, refusal("io_failed")
	}
	now, err := io.ReadAll(io.LimitReader(target, maxFileBytes+1))
	if err != nil {
		return File{}, refusal("io_failed")
	}
	if revision(now) != expected {
		return File{}, refusal("stale_revision")
	}
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	if err := parent.Rename(name, path.Base(p)); err != nil {
		return File{}, refusal("publication_failed")
	}
	return File{Path: p, Revision: revision(b)}, nil
}
