package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"golang.org/x/sys/unix"
)

// Explicit output preflight is a Task6 prerequisite before any kube write.
// Retained descriptors prevent symlink/path substitution from redirecting
// publication. Close after the run; no receipt/latest discovery is performed.
type suiteReceiptOutput struct {
	workspace, state, out, historyPath string
	history, parent                    *os.File
}
type suiteReceiptPublication struct {
	HistoryPath, ExportPath, ReceiptDigest string
	HistoryPublished, ExportPublished      bool
}

func (o *suiteReceiptOutput) Close() error {
	if o == nil {
		return nil
	}
	var failed bool
	if o.parent != nil {
		failed = o.parent.Close() != nil
	}
	if o.history != nil {
		failed = o.history.Close() != nil || failed
	}
	if failed {
		return suiteEvidenceError("output close unavailable")
	}
	return nil
}
func suitePathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func suiteAbsolutePath(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", suiteEvidenceError("invalid private path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", suiteEvidenceError("invalid private path")
	}
	return filepath.Clean(abs), nil
}

// Like the existing bundle proof reader/writer, descend with no-follow,
// nonblocking openat and retain each directory descriptor. Existing directory
// permissions are never silently repaired. Only KMX state directories created
// here are private; an explicit export's existing parent need not be private.
func openSuiteDirectory(path string, create, private bool) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, suiteEvidenceError("private directory unavailable")
	}
	dir := os.NewFile(uintptr(fd), "/")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		fd, err = unix.Openat(int(dir.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, os.ErrNotExist) && create {
			mkdirErr := unix.Mkdirat(int(dir.Fd()), component, 0700)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				dir.Close()
				return nil, suiteEvidenceError("private directory creation unavailable")
			}
			fd, err = unix.Openat(int(dir.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		}
		dir.Close()
		if err != nil {
			return nil, suiteEvidenceError("private directory unavailable")
		}
		dir = os.NewFile(uintptr(fd), component)
	}
	info, err := dir.Stat()
	if err != nil || !info.IsDir() || private && info.Mode().Perm() != 0700 {
		dir.Close()
		return nil, suiteEvidenceError("directory must be private 0700")
	}
	return dir, nil
}
func suiteDirectoryUnchanged(path string, opened *os.File, private bool) bool {
	current, err := openSuiteDirectory(path, false, private)
	if err != nil {
		return false
	}
	defer current.Close()
	a, e1 := opened.Stat()
	b, e2 := current.Stat()
	return e1 == nil && e2 == nil && os.SameFile(a, b)
}
func suiteNoFile(dir *os.File, name string) bool {
	var stat unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	return errors.Is(err, os.ErrNotExist)
}
func preflightSuiteReceiptOutput(authoredWorkspace, stateRoot, out string) (*suiteReceiptOutput, error) {
	workspace, err := suiteAbsolutePath(authoredWorkspace)
	if err != nil {
		return nil, err
	}
	state, err := suiteAbsolutePath(stateRoot)
	if err != nil {
		return nil, err
	}
	if suitePathWithin(workspace, state) || suitePathWithin(state, workspace) {
		return nil, suiteEvidenceError("history must be outside authored workspace")
	}
	work, err := openSuiteDirectory(workspace, false, false)
	if err != nil {
		return nil, err
	}
	work.Close()
	var export string
	if out != "" {
		export, err = suiteAbsolutePath(out)
		if err != nil {
			return nil, err
		}
		if suitePathWithin(workspace, export) || suitePathWithin(filepath.Join(state, "suites"), export) {
			return nil, suiteEvidenceError("export must be outside authored workspace and suite cache")
		}
	}
	// Preflight the private history destination too. Fail before native writes,
	// rather than discovering an invalid KMX_HOME after execution.
	for _, path := range []string{state, filepath.Join(state, "suite-evidence"), filepath.Join(state, "suite-evidence", "runs")} {
		dir, err := openSuiteDirectory(path, true, true)
		if err != nil {
			return nil, err
		}
		dir.Close()
	}
	historyPath := filepath.Join(state, "suite-evidence", "runs")
	history, err := openSuiteDirectory(historyPath, false, true)
	if err != nil {
		return nil, err
	}
	o := &suiteReceiptOutput{workspace: workspace, state: state, out: export, historyPath: historyPath, history: history}
	ok := false
	defer func() {
		if !ok {
			o.Close()
		}
	}()
	if export != "" {
		o.parent, err = openSuiteDirectory(filepath.Dir(export), false, false)
		if err != nil {
			return nil, err
		}
		if !suiteNoFile(o.parent, filepath.Base(export)) {
			return nil, suiteEvidenceError("export collision")
		}
	}
	// An exclusive throwaway probe checks creation/sync capability without
	// reserving the user's final path. A later collision still fails atomically.
	for _, dir := range []*os.File{o.history, o.parent} {
		if dir == nil {
			continue
		}
		name, err := randomHex(16)
		if err != nil {
			return nil, suiteEvidenceError("output preflight unavailable")
		}
		name = ".preflight-" + name
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err != nil {
			return nil, suiteEvidenceError("output preflight unavailable")
		}
		probe := os.NewFile(uintptr(fd), name)
		syncErr := probe.Sync()
		closeErr := probe.Close()
		removeErr := unix.Unlinkat(int(dir.Fd()), name, 0)
		if syncErr != nil || closeErr != nil || removeErr != nil || dir.Sync() != nil {
			return nil, suiteEvidenceError("output preflight unavailable")
		}
	}
	ok = true
	return o, nil
}

// Linkat is an atomic exclusive publication of the already synced inode, not
// rename-with-overwrite. A publication followed by failed directory sync is an
// error with Published=true: preserve honest residual evidence, never retry.
func publishSuiteBytes(dir *os.File, name string, raw []byte) (published bool, err error) {
	if len(raw) == 0 || len(raw) > suiteEvidenceMaxBytes || filepath.Base(name) != name || name == "." || name == ".." {
		return false, suiteEvidenceError("invalid receipt publication")
	}
	suffix, e := randomHex(16)
	if e != nil {
		return false, suiteEvidenceError("receipt stage unavailable")
	}
	stage := ".receipt-" + suffix
	fd, e := unix.Openat(int(dir.Fd()), stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if e != nil {
		return false, suiteEvidenceError("receipt stage unavailable")
	}
	file := os.NewFile(uintptr(fd), stage)
	defer file.Close()
	defer func() {
		if unix.Unlinkat(int(dir.Fd()), stage, 0) != nil && err == nil {
			err = suiteEvidenceError("receipt stage cleanup unavailable")
		}
	}()
	if file.Chmod(0600) != nil {
		return false, suiteEvidenceError("receipt stage permissions unavailable")
	}
	n, e := file.Write(raw)
	if e != nil || n != len(raw) || file.Sync() != nil || file.Close() != nil {
		return false, suiteEvidenceError("receipt stage write unavailable")
	}
	if unix.Linkat(int(dir.Fd()), stage, int(dir.Fd()), name, 0) != nil {
		return false, suiteEvidenceError("exclusive receipt publication unavailable")
	}
	published = true
	if dir.Sync() != nil {
		return true, suiteEvidenceError("receipt publication sync unavailable")
	}
	return true, nil
}
func publishSuiteEvaluationReceipt(s *workspace.NativeSuite, expected suiteNativeSubject, r suiteEvaluationReceipt, o *suiteReceiptOutput) (suiteReceiptPublication, error) {
	saved := suiteReceiptPublication{}
	raw, digest, err := canonicalSuiteEvaluationReceipt(s, expected, r)
	if err != nil {
		return saved, err
	}
	if o == nil || o.history == nil || !suiteDirectoryUnchanged(o.historyPath, o.history, true) {
		return saved, suiteEvidenceError("history preflight expired")
	}
	// Recheck the private parent chain as well as the retained runs directory:
	// chmod after preflight must not expose newly published history.
	for _, path := range []string{o.state, filepath.Join(o.state, "suite-evidence"), o.historyPath} {
		dir, err := openSuiteDirectory(path, false, true)
		if err != nil {
			return saved, suiteEvidenceError("history preflight expired")
		}
		if err := dir.Close(); err != nil {
			return saved, suiteEvidenceError("history preflight expired")
		}
	}
	name := "eval-" + r.RunID + ".json"
	saved.HistoryPath = filepath.Join(o.historyPath, name)
	saved.ExportPath = o.out
	saved.ReceiptDigest = digest
	saved.HistoryPublished, err = publishSuiteBytes(o.history, name, raw)
	if err != nil {
		return saved, err
	}
	if o.out == "" {
		return saved, nil
	}
	if o.out == saved.HistoryPath {
		saved.ExportPublished = true
		return saved, nil
	}
	if o.parent == nil || !suiteDirectoryUnchanged(filepath.Dir(o.out), o.parent, false) {
		return saved, suiteEvidenceError("export preflight expired; history retained")
	}
	saved.ExportPublished, err = publishSuiteBytes(o.parent, filepath.Base(o.out), raw)
	if err != nil {
		return saved, suiteEvidenceError("export unavailable; history retained")
	}
	return saved, nil
}
func readSuitePrivateJSON(path string) ([]byte, error) {
	absolute, err := suiteAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	dir, err := openSuiteDirectory(filepath.Dir(absolute), false, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := openKagentFile(dir, filepath.Base(absolute))
	if err != nil {
		return nil, suiteEvidenceError("private file unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() <= 0 || before.Size() > suiteEvidenceMaxBytes {
		return nil, suiteEvidenceError("file must be bounded private 0600")
	}
	raw, err := io.ReadAll(io.LimitReader(file, suiteEvidenceMaxBytes+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(raw) > suiteEvidenceMaxBytes || int64(len(raw)) != before.Size() || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return nil, suiteEvidenceError("private file changed or unavailable")
	}
	return raw, nil
}
func readSuiteEvaluationReceiptFile(path string) (suiteEvaluationReceipt, error) {
	raw, err := readSuitePrivateJSON(path)
	if err != nil {
		return suiteEvaluationReceipt{}, err
	}
	return decodeSuiteEvaluationReceipt(raw)
}
func readSuitePromotionPolicyFile(path string) (suitePromotionPolicy, error) {
	raw, err := readSuitePrivateJSON(path)
	if err != nil {
		return suitePromotionPolicy{}, err
	}
	return decodeSuitePromotionPolicy(raw)
}
