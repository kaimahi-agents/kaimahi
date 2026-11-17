//go:build windows

package fileaccess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

type cancelAfterChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func TestWindowsWalkChecksCancellationBetweenSteps(t *testing.T) {
	dir := foundationTempDir(t)
	nested := filepath.Join(dir, "one", "two", "three")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterChecks{Context: context.Background(), remaining: 8}
	if opened, err := OpenDirectory(ctx, nested); !errors.Is(err, context.Canceled) {
		if err == nil {
			opened.Close()
		}
		t.Fatalf("walk cancellation = %v", err)
	}
}

func TestWindowsOpenRefusesJunctions(t *testing.T) {
	base := foundationTempDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	makeJunction(t, link, target)
	for _, path := range []string{link, link + `\child`, `\\?\` + link} {
		if file, err := OpenDirectory(t.Context(), path); !errors.Is(err, ErrUnsafe) {
			if err == nil {
				file.Close()
			}
			t.Errorf("OpenDirectory(junction) = %v", err)
		}
	}
	parent := openTestDirectory(t, base)
	if file, err := OpenDirectoryAt(t.Context(), parent, "junction"); !errors.Is(err, ErrUnsafe) {
		if err == nil {
			file.Close()
		}
		t.Errorf("OpenDirectoryAt(junction) = %v", err)
	}
	reparse := &Directory{file: openReparsePoint(t, link)}
	if err := CheckPrivate(t.Context(), reparse, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("CheckPrivate(junction) = %v", err)
	}
	if _, err := PhysicalIdentity(t.Context(), reparse); !errors.Is(err, ErrUnsafe) {
		t.Errorf("PhysicalIdentity(junction) = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 1 {
		t.Fatalf("junction target changed: %v, %v", entries, err)
	}
}

func TestWindowsOpenRefusesSymbolicLinksWhenAvailable(t *testing.T) {
	dir := foundationTempDir(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("symbolic-link privilege is unavailable; junction coverage is mandatory and already ran")
		}
		t.Fatal(err)
	}
	parent := openTestDirectory(t, dir)
	if file, err := OpenRegularAt(t.Context(), parent, "link"); !errors.Is(err, ErrUnsafe) {
		if err == nil {
			file.Close()
		}
		t.Errorf("OpenRegularAt(symbolic link) = %v", err)
	}
}

func TestWindowsOpenRefusesAliasesAndStreams(t *testing.T) {
	dir := foundationTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data:secret"), []byte("stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := openTestDirectory(t, dir)
	for _, name := range []string{"data:secret", "data::$DATA", "data.", "data ", "CON", "nul.txt", "COM1"} {
		if file, err := OpenRegularAt(t.Context(), parent, name); !errors.Is(err, ErrUnsafe) {
			if err == nil {
				file.Close()
			}
			t.Errorf("OpenRegularAt(%q) = %v, want unsafe", name, err)
		}
	}
	for _, name := range []string{"sub.", "sub ", "sub::$INDEX_ALLOCATION", "AUX"} {
		if file, err := OpenDirectoryAt(t.Context(), parent, name); !errors.Is(err, ErrUnsafe) {
			if err == nil {
				file.Close()
			}
			t.Errorf("OpenDirectoryAt(%q) = %v, want unsafe", name, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, "data:secret")); err != nil || string(got) != "stream" {
		t.Fatalf("stream fixture changed: %q, %v", got, err)
	}
}

func TestWindowsAnchoredReadSurvivesPathReplacement(t *testing.T) {
	base := foundationTempDir(t)
	original := filepath.Join(base, "original")
	moved := filepath.Join(base, "moved")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "value"), []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle := openTestDirectory(t, original)
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "value"), []byte("redirected"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, original, target)
	file, err := OpenRegularAt(t.Context(), handle, "value")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	file.Close()
	if readErr != nil || string(data) != "held" {
		t.Fatalf("anchored read = %q, %v", data, readErr)
	}
	if _, err := OpenDirectory(t.Context(), original); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("replacement junction = %v", err)
	}
}

func TestWindowsIdentityAliases(t *testing.T) {
	dir := filepath.Join(foundationTempDir(t), "LongDirectoryNameForIdentity")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dir, "LongFileNameForIdentity.txt")
	if err := os.WriteFile(filePath, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := identityForWindowsPath(t, dir, filepath.Base(filePath))
	base := filepath.Dir(dir)
	t.Chdir(base)
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	aliases := [][2]string{
		{`\\?\` + dir, filepath.Base(filePath)},
		{strings.ToUpper(dir), strings.ToUpper(filepath.Base(filePath))},
		{strings.ReplaceAll(dir, `\`, `/`), filepath.Base(filePath)},
		{rel, filepath.Base(filePath)},
	}
	for _, alias := range aliases {
		if got := identityForWindowsPath(t, alias[0], alias[1]); got != want {
			t.Errorf("identity through %q = %q, want %q", alias[0], got, want)
		}
	}
	short := shortPath(t, filePath)
	if !strings.EqualFold(short, filePath) {
		if got := identityForWindowsPath(t, filepath.Dir(short), filepath.Base(short)); got != want {
			t.Errorf("8.3 identity = %q, want %q", got, want)
		}
	} else {
		t.Log("8.3 short names are not generated on this volume")
	}
}

func TestWindowsPhysicalContainmentCollapsesAliases(t *testing.T) {
	root := filepath.Join(foundationTempDir(t), "LongWorkspaceNameForContainment")
	child := filepath.Join(root, "nested")
	outside := filepath.Join(filepath.Dir(root), "outside")
	for _, directory := range []string{child, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	short := shortPath(t, root)
	for _, candidate := range []string{strings.ToUpper(child), filepath.Join(short, "nested"), filepath.Join(short, "new", "receipt.json")} {
		inside, err := PhysicallyContains(t.Context(), root, candidate)
		if err != nil || !inside {
			t.Fatalf("containment through %q = %t, %v", candidate, inside, err)
		}
	}
	inside, err := PhysicallyContains(t.Context(), root, outside)
	if err != nil || inside {
		t.Fatalf("outside containment = %t, %v", inside, err)
	}
}

func identityForWindowsPath(t *testing.T, dir, name string) string {
	t.Helper()
	parent := openTestDirectory(t, dir)
	file, err := OpenRegularAt(t.Context(), parent, name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	id, err := PhysicalIdentity(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWindowsPhysicalIdentityRefusesNonDiskObjects(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := PhysicalIdentity(t.Context(), &File{file: read}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("PhysicalIdentity(pipe) = %v", err)
	}
	base := foundationTempDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	makeJunction(t, link, target)
	file := &Directory{file: openReparsePoint(t, link)}
	if _, err := PhysicalIdentity(t.Context(), file); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("PhysicalIdentity(junction) = %v", err)
	}
}
