// Package mcp adapts local agent-file operations to stdio MCP.
// File access is agent-only, optionally root-bound; execution uses shared App snapshots.
// Unbound non-fake previews default to Copilot's fixed endpoint and the authored
// model; explicit routes, fake previews and legacy root-bound behavior are unchanged.
// Tool requests must not choose model endpoints or substitute a runner.
// Human-readable tool titles leave protocol names and argument schemas unchanged.
// The canonical resource includes an exact, revision-bound authoring snapshot.
// Optional failure-only excerpts exclude passing and unknown answers from safe
// results and observations; broader disclosure remains explicitly requested.
// Optional native v2 branches use a trusted installed Squad program for guarded
// init/cast, operation-owned starter observations and byte-preserving enrichment.
// Reopened member operations revalidate native casting state, never infer origin
// or persist kmx mapping. Preview shares App snapshots, not native dispatch.
// Native partial effects remain explicit; no extra tool, downloader or canvas actions.
// Canonical Edit optionally retains bounded private source preimages. Retention
// failure never blocks a valid save and is explicit in its successful result.
// Source-only versions are read-only resources; restore uses the same Edit CAS.
// Linux creation uses no-replace rename, then hard-link publication when needed.
// Mounts lacking both use exclusive creation and synced copy without atomic visibility.
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
		return "publication_failed: file publication failed"
	case "cancelled":
		return "cancelled: operation stopped before publication"
	case "deadline_exceeded":
		return "deadline_exceeded: operation deadline elapsed"
	case "history_unavailable":
		return "history_unavailable: history is busy or unsafe; stop MCP for this root and retry, or remove its private versions directory manually; no current files were changed"
	case "squad_setup_failed":
		return "squad_setup_failed: native setup failed; inspect retained files before reconciliation; no automatic retry or rollback"
	case "squad_not_setup":
		return "squad_not_setup: not set up by kmx; native member state could not be validated; no origin is inferred"
	case "squad_starter_changed":
		return "squad_starter_changed: inspect the native member and partial effects manually; no automatic retry or adoption"
	case "squad_enrichment_failed":
		return "squad_enrichment_failed: inspect the registered native member and partial effects manually; do not repeat cast or use canonical Edit"
	default:
		return e.Code + ": operation refused"
	}
}
func refusal(code string) error { return &Error{Code: code} }

// File identifies exact saved bytes without returning their content.
type File struct {
	Path     string             `json:"path"`
	Revision string             `json:"revision"`
	History  *FileHistoryStatus `json:"history,omitempty"`
}

// Files holds an optional selected root with a caller-selected validator.
// A nil root resolves agent paths against cwd; explicit roots retain legacy policy.
// v1 uses canonical parsing; native cast operations bind their explicit profile.
type Files struct {
	root        *os.Root
	validate    func(string, []byte) error
	viewMu      sync.Mutex
	latest      previewObservation
	historyDir  func() (string, error)
	historyGate chan struct{}
}

func OpenFiles(root string, validate func(string, []byte) error) (*Files, error) {
	if validate == nil {
		return nil, refusal("invalid_options")
	}
	if root == "" {
		return &Files{validate: validate}, nil
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

// OpenFilesWithHistory enables best-effort canonical Edit retention. The trusted
// directory resolver is lazy: startup and help never create history or read config.
func OpenFilesWithHistory(root string, validate func(string, []byte) error, directory func() (string, error)) (*Files, error) {
	if directory == nil {
		return nil, refusal("invalid_options")
	}
	f, err := OpenFiles(root, validate)
	if err != nil {
		return nil, err
	}
	f.historyDir, f.historyGate = directory, make(chan struct{}, 1)
	return f, nil
}

func (f *Files) Close() error {
	if f.root == nil {
		return nil
	}
	return f.root.Close()
}
func revision(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

// rootRelative resolves aliases to a canonical target, relative to an explicit
// root or absolute when unbound. Missing suffixes support Create; an existing
// dangling link never becomes one. IO uses the retained root/parent descriptor.
func (f *Files) rootRelative(p string) (string, error) {
	if f.root == nil {
		if !agentPath(p) {
			return "", refusal("unsafe_path")
		}
		var err error
		p, err = filepath.Abs(p)
		if err != nil {
			return "", refusal("unsafe_path")
		}
	}
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
	if f.root == nil {
		if !agentPath(resolved) {
			return "", refusal("unsafe_path")
		}
		return resolved, nil
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

// Both the requested name and resolved target must identify an agent file.
// Directory/member names are not authored agent IDs and need no portable-ID rule.
func agentPath(p string) bool {
	name := filepath.Base(p)
	if strings.HasSuffix(name, ".agent.md") && name != ".agent.md" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	n := len(parts)
	return n >= 4 && parts[n-4] == ".squad" && parts[n-3] == "agents" && parts[n-2] != "" && parts[n-1] == "charter.md"
}

func (f *Files) allowedPath(p string) bool {
	if f.root == nil {
		return agentPath(p)
	}
	return safePath(p)
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
	if !f.allowedPath(p) {
		return refusal("unsafe_path")
	}
	if len(b) == 0 || len(b) > maxFileBytes || f.validate(p, b) != nil {
		return refusal("invalid_file")
	}
	return nil
}
func (f *Files) parent(p string, create bool) (*os.Root, error) {
	if f.root == nil {
		dir := filepath.Dir(p)
		if create {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, refusal("io_failed")
			}
		}
		r, err := os.OpenRoot(dir)
		if os.IsNotExist(err) {
			return nil, refusal("not_found")
		}
		if err != nil {
			return nil, refusal("unsafe_path")
		}
		return r, nil
	}
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
	requested := p
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
	if err := renameExclusive(parent, name, filepath.Base(p)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return File{}, refusal("already_exists")
		}
		return File{}, refusal("publication_failed")
	}
	if f.root == nil {
		p = requested
	}
	return File{Path: p, Revision: revision(b)}, nil
}
func (f *Files) Edit(ctx context.Context, p, expected string, b []byte) (File, error) {
	return f.editObserved(ctx, p, expected, b, nil)
}

func (f *Files) editObserved(ctx context.Context, p, expected string, b []byte, observed *sourceIdentity) (File, error) {
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	requested := p
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
	if observed != nil && !f.observedParent(p, parent, observed) {
		return File{}, refusal("stale_revision")
	}
	target, old, info, err := readTarget(parent, filepath.Base(p))
	if err != nil {
		return File{}, err
	}
	defer target.Close()
	if observed != nil && !os.SameFile(observed.file, info) {
		return File{}, refusal("stale_revision")
	}
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
	check := func() ([]byte, error) {
		current, err := parent.Lstat(filepath.Base(p))
		if err != nil || !os.SameFile(info, current) || current.Mode() != info.Mode() || !singleLink(target) ||
			(observed != nil && !f.observedParent(p, parent, observed)) {
			return nil, refusal("stale_revision")
		}
		if _, err := target.Seek(0, io.SeekStart); err != nil {
			return nil, refusal("io_failed")
		}
		now, err := io.ReadAll(io.LimitReader(target, maxFileBytes+1))
		if err != nil {
			return nil, refusal("io_failed")
		}
		if revision(now) != expected {
			return nil, refusal("stale_revision")
		}
		return now, nil
	}
	now, err := check()
	if err != nil {
		return File{}, err
	}
	var history *FileHistoryStatus
	if f.historyDir != nil {
		store, status := f.retainHistory(ctx, p, now)
		if store != nil {
			defer store.close()
		}
		history = status
		// History IO grants no authority to overwrite a newly changed source.
		if _, err := check(); err != nil {
			return File{}, err
		}
	}
	if err := cancelled(ctx); err != nil {
		return File{}, err
	}
	if err := parent.Rename(name, filepath.Base(p)); err != nil {
		return File{}, refusal("publication_failed")
	}
	if f.root == nil {
		p = requested
	}
	return File{Path: p, Revision: revision(b), History: history}, nil
}
