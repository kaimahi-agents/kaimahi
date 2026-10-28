package agentfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// pairObservation retains handle-derived identities. In particular, Windows
// Lstat may lazily reopen its path for identity; retained f.Stat snapshots do not.
// This is cooperative replacement detection, not a hostile-filesystem sandbox.
// A changed parent timestamp may conservatively stale unrelated child operations;
// it is a version signal, not an exhaustive directory event log.
type pairObservation struct {
	path, parent         string
	fileInfo, parentInfo os.FileInfo
}

func (o *pairObservation) same(other *pairObservation) bool {
	if o == nil || other == nil {
		return o == nil && other == nil
	}
	if o.path != other.path || o.parent != other.parent || !os.SameFile(o.parentInfo, other.parentInfo) || !o.parentInfo.ModTime().Equal(other.parentInfo.ModTime()) {
		return false
	}
	if o.fileInfo == nil || other.fileInfo == nil {
		return o.fileInfo == nil && other.fileInfo == nil
	}
	return samePairFileVersion(o.fileInfo, other.fileInfo)
}

// LoadPair reads bounded CLI-style sources without changing Load or any existing
// consumer. Relative overrides use cwd. Only a missing default source is absence;
// no fallback search, symlinks, reparse points, parent traversal or special files.
func LoadPair(agentFile, testsOverride string) (*Pair, error) {
	agentPath, err := pairAbsolute(agentFile, "File")
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(filepath.Base(agentPath), ".agent.test.md") {
		return nil, refuse(agentFile, 1, 1, "Tests", "select an agent file, not a tests file")
	}
	target, supported := DefaultTestsPath(agentPath)
	explicit := testsOverride != ""
	if explicit {
		if !supported {
			return nil, refuse(agentFile, 1, 1, "Tests", "select a supported *.agent.md file for companion tests")
		}
		target, err = pairAbsolute(testsOverride, "Tests")
		if err != nil {
			return nil, err
		}
	}
	if target == agentPath {
		return nil, refuse(agentFile, 1, 1, "Tests", "select different agent and tests files")
	}
	agentBytes, agentObs, err := readPairSource(agentPath, "File", false)
	if err != nil {
		return nil, err
	}
	agent, err := Parse(agentFile, agentBytes)
	if err != nil {
		return nil, err
	}
	var testsBytes []byte
	var testsObs *pairObservation
	if target != "" {
		testsBytes, testsObs, err = readPairSource(target, "Tests", !explicit)
		if err != nil {
			return nil, err
		}
	}
	if testsObs != nil && testsObs.fileInfo != nil && os.SameFile(agentObs.fileInfo, testsObs.fileInfo) {
		return nil, refuse(agentFile, 1, 1, "Tests", "select different agent and tests files")
	}
	// Both readers are bounded at 256 KiB, so their combined raw input is at most
	// 512 KiB. Each source is read and hashed once, never serialized through YAML.
	p, err := ComposePair(agentPath, agent, TestsSelection{Path: target, Explicit: explicit}, testsBytes, testsObs != nil && testsObs.fileInfo != nil)
	if err != nil {
		return nil, err
	}
	// Recheck both paths after composition, so a change while reading the second
	// source cannot be mistaken for a checked two-source snapshot.
	if err := agentObs.check("File"); err != nil {
		return nil, err
	}
	if testsObs != nil {
		if err := testsObs.check("Tests"); err != nil {
			return nil, err
		}
	}
	p.agentObservation, p.testsObservation = agentObs, testsObs
	return p, nil
}

func hasParentTraversal(filename string) bool {
	path := filename[len(filepath.VolumeName(filename)):]
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func pairAbsolute(filename, field string) (string, error) {
	if filename == "" {
		return "", refuse(filename, 1, 1, field, "select an explicit agent file")
	}
	if hasParentTraversal(filename) {
		return "", refuse(filename, 1, 1, field, "select a file path without parent traversal")
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", refuse(filename, 1, 1, field, "select a readable regular file")
	}
	return absolute, nil
}

// inspectPairPath visits every existing ancestor, including beneath an absent
// intermediate path. Only ErrNotExist is absence, never ENOTDIR or permission.
func inspectPairPath(path, field string) (*pairObservation, error) {
	fail := func(problem string) (*pairObservation, error) { return nil, refuse(path, 1, 1, field, problem) }
	o := &pairObservation{path: path}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return fail("select a readable regular file with directory ancestors")
			}
		} else {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return fail("select a file with no symlinks or reparse points in its path")
			}
			if p == path {
				o.fileInfo = info
			} else {
				if !info.IsDir() {
					return fail("select a file with directory ancestors")
				}
				if o.parent == "" {
					o.parent, o.parentInfo = p, info
				}
			}
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if o.parentInfo == nil {
		return fail("select a file with an existing directory ancestor")
	}
	return o, nil
}

func readPairSource(path, field string, allowMissing bool) ([]byte, *pairObservation, error) {
	fail := func(problem string) ([]byte, *pairObservation, error) {
		return nil, nil, refuse(path, 1, 1, field, problem)
	}
	o, err := inspectPairPath(path, field)
	if err != nil {
		return nil, nil, err
	}
	if o.fileInfo == nil && !allowMissing {
		return fail("select an existing readable regular file")
	}
	if o.fileInfo != nil && (!o.fileInfo.Mode().IsRegular() || o.fileInfo.Size() > MaxFileBytes) {
		return fail("select a regular file of at most 256 KiB")
	}
	parent, err := os.Open(o.parent)
	if err != nil {
		return fail("select a readable parent directory")
	}
	defer parent.Close()
	openedParent, err := parent.Stat()
	if err != nil || !openedParent.IsDir() || !os.SameFile(o.parentInfo, openedParent) || !o.parentInfo.ModTime().Equal(openedParent.ModTime()) {
		return fail("parent directory changed while opening; select a stable source")
	}
	o.parentInfo = openedParent
	if o.fileInfo == nil {
		return nil, o, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fail("select a readable regular file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !samePairFileVersion(o.fileInfo, opened) {
		return fail("file changed while opening; select a stable regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes || int64(len(data)) != opened.Size() {
		return fail("file changed or exceeds 256 KiB; select a stable bounded file")
	}
	after, err := f.Stat()
	if err != nil || !samePairFileVersion(opened, after) {
		return fail("file changed while reading; select a stable regular file")
	}
	o.fileInfo = opened
	return data, o, nil
}

func samePairFileVersion(a, b os.FileInfo) bool {
	return b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (o *pairObservation) check(field string) error {
	current, err := inspectPairPath(o.path, field)
	if err != nil {
		return err
	}
	if !o.same(current) {
		return refuse(o.path, 1, 1, field, "source or parent changed while reading; select a stable source")
	}
	return nil
}
