//go:build linux || darwin

package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func historyOwned(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1
}

func historyOpenFile(root *os.Root, name string, flags int) (*os.File, error) {
	return root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
}

// The operator-selected state directory may itself be an alias. Resolve that
// boundary once; only the owned history components must be symlink-free.
func openHistory(base, operationRoot string, create bool) (_ *historyStore, err error) {
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return nil, refusal("history_unavailable")
	}
	state := filepath.Dir(filepath.Dir(base))
	if create {
		if err := os.MkdirAll(state, 0700); err != nil {
			return nil, refusal("history_unavailable")
		}
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		if !create && os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, refusal("history_unavailable")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return nil, refusal("history_unavailable")
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, refusal("history_unavailable")
	}
	s := &historyStore{roots: []*os.Root{root}, statePath: state, stateInfo: info, limits: historyLimits{perFile: 20, records: 200, bytes: 32 << 20}}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	for _, name := range []string{filepath.Base(filepath.Dir(base)), filepath.Base(base), historyHash(operationRoot)} {
		info, statErr := root.Lstat(name)
		if os.IsNotExist(statErr) && create {
			if mkdirErr := root.Mkdir(name, 0700); mkdirErr != nil && !os.IsExist(mkdirErr) {
				return nil, refusal("history_unavailable")
			}
			info, statErr = root.Lstat(name)
		}
		if !create && os.IsNotExist(statErr) {
			return nil, os.ErrNotExist
		}
		if statErr != nil || !historyOwned(info, true) {
			return nil, refusal("history_unavailable")
		}
		next, openErr := root.OpenRoot(name)
		if openErr != nil {
			return nil, refusal("history_unavailable")
		}
		s.roots = append(s.roots, next)
		opened, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			return nil, refusal("history_unavailable")
		}
		s.anchors = append(s.anchors, historyAnchor{parent: root, name: name, info: info})
		root = next
	}
	s.root = root
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	s.lock, err = historyOpenFile(root, "lock", flags)
	if create && errors.Is(err, os.ErrExist) {
		s.lock, err = historyOpenFile(root, "lock", os.O_RDWR)
	}
	if err != nil {
		return nil, refusal("history_unavailable")
	}
	s.lockInfo, err = s.lock.Stat()
	if err != nil || !historyOwned(s.lockInfo, false) || s.lockInfo.Size() != 0 {
		return nil, refusal("history_unavailable")
	}
	s.unlock, err = lockTarget(s.lock)
	if err != nil {
		return nil, refusal("history_unavailable")
	}
	if !s.unchanged() {
		return nil, refusal("history_unavailable")
	}
	return s, nil
}
