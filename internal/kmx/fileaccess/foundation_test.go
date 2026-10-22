package fileaccess

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Normal fixtures need physical roots; deliberate symlink inputs stay unresolved.
func faTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func faOpenDir(t *testing.T, path string) *os.File {
	t.Helper()
	dir, err := OpenDirectory(path)
	if err != nil {
		t.Fatalf("OpenDirectory(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

func faSameFile(t *testing.T, file *os.File, path string) {
	t.Helper()
	got, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat opened %s: %v", path, err)
	}
	want, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat %s: %v", path, err)
	}
	if !os.SameFile(got, want) {
		t.Fatalf("opened handle is not %s", path)
	}
}

func TestOpenDirectoryOpensExistingDirectory(t *testing.T) {
	dir := faTempDir(t)
	if err := faWriteFile(filepath.Join(dir, "entry"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opened := faOpenDir(t, dir)
	faSameFile(t, opened, dir)
	entries, err := opened.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != "entry" {
		t.Fatalf("ReadDir = %v, %v; want [entry]", entries, err)
	}
}

func TestOpenDirectoryRelativePath(t *testing.T) {
	dir := faTempDir(t)
	// The checkout and temporary directory may be on different Windows
	// volumes. Relative-path coverage needs a same-volume working directory.
	t.Chdir(filepath.Dir(dir))
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Fatalf("Rel(%s, %s): %v", wd, dir, err)
	}
	faSameFile(t, faOpenDir(t, rel), dir)
	faSameFile(t, faOpenDir(t, filepath.Join(rel, "..", filepath.Base(dir))), dir)
}

func TestOpenDirectoryMissingIsNotExist(t *testing.T) {
	dir := faTempDir(t)
	for _, path := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "missing", "child")} {
		if _, err := OpenDirectory(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("OpenDirectory(%s) = %v, want not-exist", path, err)
		}
	}
}

func TestOpenDirectoryRefusesRegularFile(t *testing.T) {
	file := filepath.Join(faTempDir(t), "file")
	if err := faWriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(file, "child")} {
		opened, err := OpenDirectory(path)
		if err == nil {
			opened.Close()
			t.Fatalf("OpenDirectory(%s) succeeded on a regular file", path)
		}
		if errors.Is(err, fs.ErrNotExist) && path == file {
			t.Fatalf("OpenDirectory(%s) = %v, a file must not look absent", path, err)
		}
	}
}

func TestOpenDirectoryAtOpensSingleComponent(t *testing.T) {
	dir := faTempDir(t)
	child := filepath.Join(dir, "child")
	if err := fixtureMkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenDirectoryAt(faOpenDir(t, dir), "child")
	if err != nil {
		t.Fatalf("OpenDirectoryAt: %v", err)
	}
	defer opened.Close()
	faSameFile(t, opened, child)
}

func TestOpenAtRefusesMultiComponentNames(t *testing.T) {
	dir := faTempDir(t)
	if err := fixtureMkdir(filepath.Join(dir, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureMkdir(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := faWriteFile(filepath.Join(dir, "a", "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parent := faOpenDir(t, filepath.Join(dir, "a"))
	names := []string{"", ".", "..", "b/", "../a", "b/../b", "x\x00"}
	if filepath.Separator == '\\' {
		names = append(names, `b\`, `..\a`)
	}
	for _, name := range names {
		if f, err := OpenDirectoryAt(parent, name); err == nil {
			f.Close()
			t.Errorf("OpenDirectoryAt(%q) succeeded", name)
		}
		if f, err := OpenRegularAt(parent, name, os.O_RDONLY); err == nil {
			f.Close()
			t.Errorf("OpenRegularAt(%q) succeeded", name)
		}
	}
}

func TestOpenRegularAt(t *testing.T) {
	dir := faTempDir(t)
	if err := faWriteFile(filepath.Join(dir, "file"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixtureMkdir(filepath.Join(dir, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := faOpenDir(t, dir)

	read, err := OpenRegularAt(parent, "file", os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenRegularAt read: %v", err)
	}
	data, err := io.ReadAll(read)
	read.Close()
	if err != nil || string(data) != "old" {
		t.Fatalf("read = %q, %v", data, err)
	}
	writeOnly, err := OpenRegularAt(parent, "file", os.O_WRONLY)
	if err != nil {
		t.Fatalf("OpenRegularAt write-only: %v", err)
	}
	if info, err := writeOnly.Stat(); err != nil || info.Size() != 3 {
		t.Fatalf("access-only open changed size: %v, %v", info, err)
	}
	writeOnly.Close()
	write, err := OpenRegularAt(parent, "file", os.O_RDWR)
	if err != nil {
		t.Fatalf("OpenRegularAt write: %v", err)
	}
	if _, err := write.WriteAt([]byte("new"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	write.Close()
	if data, _ := os.ReadFile(filepath.Join(dir, "file")); string(data) != "new" {
		t.Fatalf("written = %q", data)
	}
	if f, err := OpenRegularAt(parent, "dir", os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("OpenRegularAt opened a directory")
	}
	if _, err := OpenRegularAt(parent, "missing", os.O_RDONLY); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenRegularAt missing = %v, want not-exist", err)
	}
	for _, flags := range []int{os.O_WRONLY | os.O_RDWR, os.O_CREATE, os.O_EXCL, os.O_TRUNC, os.O_APPEND, os.O_RDWR | os.O_CREATE, os.O_RDWR | os.O_TRUNC, os.O_WRONLY | os.O_APPEND} {
		if f, err := OpenRegularAt(parent, "file", flags); err == nil {
			f.Close()
			t.Errorf("OpenRegularAt flags %#x succeeded", flags)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "file")); string(data) != "new" {
		t.Fatalf("refused flags changed file to %q", data)
	}
	for _, flags := range []int{os.O_RDONLY, os.O_WRONLY, os.O_RDWR, os.O_CREATE, os.O_CREATE | os.O_EXCL} {
		if f, err := OpenRegularAt(parent, "absent", flags); err == nil {
			f.Close()
			t.Fatalf("missing file open %#x succeeded", flags)
		}
		if _, err := os.Lstat(filepath.Join(dir, "absent")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing open created an object: %v", err)
		}
	}
}

func TestPhysicalIdentity(t *testing.T) {
	dir := faTempDir(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, path := range []string{a, b} {
		if err := faWriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "hardlink")
	if err := os.Link(a, link); err != nil {
		t.Fatalf("Link: %v", err)
	}
	parent := faOpenDir(t, dir)
	id := func(name string) string {
		t.Helper()
		f, err := OpenRegularAt(parent, name, os.O_RDONLY)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got, err := PhysicalIdentity(f)
		if err != nil || got == "" {
			t.Fatalf("PhysicalIdentity(%s) = %q, %v", name, got, err)
		}
		return got
	}
	idA := id("a")
	if again := id("a"); again != idA {
		t.Fatalf("identity changed between handles: %s vs %s", idA, again)
	}
	if idB := id("b"); idB == idA {
		t.Fatalf("distinct files share identity %s", idA)
	}
	if idLink := id("hardlink"); idLink != idA {
		t.Fatalf("hard link identity %s, want %s", idLink, idA)
	}
	if err := os.Rename(a, filepath.Join(dir, "renamed")); err != nil {
		t.Fatal(err)
	}
	if renamed := id("renamed"); renamed != idA {
		t.Fatalf("rename changed identity: %s vs %s", renamed, idA)
	}
	dirID, err := PhysicalIdentity(parent)
	if err != nil || dirID == idA {
		t.Fatalf("directory identity = %q, %v", dirID, err)
	}
	closed, err := OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if _, err := PhysicalIdentity(closed); err == nil {
		t.Fatal("PhysicalIdentity on a closed file succeeded")
	}
}

func TestCheckFunctionsRefuseClosedAndNonDiskFiles(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	defer read.Close()
	if err := CheckPrivate(read, false); err == nil {
		t.Error("CheckPrivate accepted a pipe")
	}
	if err := CheckSafeDirectory(read, false); err == nil {
		t.Error("CheckSafeDirectory accepted a pipe")
	}
	closed, err := OpenDirectory(faTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := CheckPrivate(closed, true); err == nil {
		t.Error("CheckPrivate accepted a closed file")
	}
	if err := CheckSafeDirectory(closed, true); err == nil {
		t.Error("CheckSafeDirectory accepted a closed file")
	}
}
