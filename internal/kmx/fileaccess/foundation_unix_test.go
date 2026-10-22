//go:build !windows

package fileaccess

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenRefusesSymlinksAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "file"), filepath.Join(dir, "filelink")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "dirlink"), filepath.Join(dir, "dirlink", "x")} {
		if f, err := OpenDirectory(path); err == nil {
			f.Close()
			t.Errorf("OpenDirectory(%s) followed a symlink", path)
		}
	}
	parent := faOpenDir(t, dir)
	if f, err := OpenDirectoryAt(parent, "dirlink"); err == nil {
		f.Close()
		t.Error("OpenDirectoryAt followed a symlink")
	}
	for _, name := range []string{"filelink", "fifo"} {
		// O_NONBLOCK keeps a FIFO open from blocking; the type check refuses it.
		if f, err := OpenRegularAt(parent, name, os.O_RDONLY); err == nil {
			f.Close()
			t.Errorf("OpenRegularAt(%s) succeeded", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "file")); err != nil {
		t.Fatal(err)
	}
}

func TestUnixCheckPrivateModes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		mode os.FileMode
		dir  bool
		ok   bool
	}{
		{0o600, false, true}, {0o400, false, true}, {0o640, false, false}, {0o604, false, false},
		{0o700, true, true}, {0o750, true, false}, {0o701, true, false},
	} {
		path := filepath.Join(dir, fmt.Sprintf("%v-%o", tc.dir, tc.mode))
		if tc.dir {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range []bool{false, true} {
			if err := CheckPrivate(f, root); (err == nil) != tc.ok {
				t.Errorf("CheckPrivate(%o dir=%v root=%v) = %v, want ok=%v", tc.mode, tc.dir, root, err, tc.ok)
			}
		}
		f.Close()
	}
}

func TestUnixCheckSafeDirectoryModes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		mode                os.FileMode
		ancestor, immediate bool
	}{
		{0o755, true, true}, {0o700, true, true}, {0o775, false, false}, {0o757, false, false},
		{0o777 | os.ModeSticky, true, false}, {0o755 | os.ModeSticky, true, true},
	} {
		path := filepath.Join(dir, fmt.Sprintf("%o", uint32(tc.mode)))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		f := faOpenDir(t, path)
		if err := CheckSafeDirectory(f, false); (err == nil) != tc.ancestor {
			t.Errorf("CheckSafeDirectory(%v, ancestor) = %v", tc.mode, err)
		}
		if err := CheckSafeDirectory(f, true); (err == nil) != tc.immediate {
			t.Errorf("CheckSafeDirectory(%v, immediate) = %v", tc.mode, err)
		}
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := CheckSafeDirectory(f, false); err == nil {
		t.Error("CheckSafeDirectory accepted a regular file")
	}
}
