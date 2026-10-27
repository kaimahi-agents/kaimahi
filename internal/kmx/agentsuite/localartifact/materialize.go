package localartifact

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path"
	"sort"
)

// Materialize writes validated content into a new caller-owned directory. Files
// and hardlinks precede symlinks so writes never traverse an authored symlink.
// Directory permissions are applied last; group/world write bits are removed.
// Declared modes must support owner reads/traversal before any staging is created.
func (a *Artifact) Materialize(ctx context.Context, output string) error {
	if a == nil || a.entries == nil {
		return ErrArtifact
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, e := range a.entries {
		if e.Kind == tar.TypeReg && e.Mode&0400 == 0 || e.Kind == tar.TypeDir && e.Mode&0500 != 0500 {
			return errors.New("artifact modes do not support owner-readable files and owner-readable/searchable directories for build staging")
		}
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	r, err := os.OpenRoot(output)
	if err != nil {
		return err
	}
	defer r.Close()
	var names, dirs []string
	for name, e := range a.entries {
		names = append(names, name)
		parent := path.Dir(name)
		if e.Kind == tar.TypeDir {
			parent = name
			dirs = append(dirs, name)
		}
		if err := r.MkdirAll(parent, 0700); err != nil {
			return err
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := a.entries[name]
		if e.Kind != tar.TypeReg {
			continue
		}
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(a.files[name])
		modeErr := f.Chmod(os.FileMode(e.Mode) & 0777 &^ 0022)
		if err := errors.Join(writeErr, modeErr, f.Close()); err != nil {
			return err
		}
	}
	for _, name := range names {
		e := a.entries[name]
		if e.Kind != tar.TypeLink {
			continue
		}
		if err := r.Link(e.Link, name); err != nil {
			return err
		}
	}
	for _, name := range names {
		e := a.entries[name]
		if e.Kind != tar.TypeSymlink {
			continue
		}
		if err := r.Symlink(e.Link, name); err != nil {
			return err
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, name := range dirs {
		if err := r.Chmod(name, os.FileMode(a.entries[name].Mode)&0777&^0022); err != nil {
			return err
		}
	}
	return ctx.Err()
}
