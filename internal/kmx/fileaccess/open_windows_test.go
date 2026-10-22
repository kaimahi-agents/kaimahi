//go:build windows

package fileaccess

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func faSetACL(t *testing.T, path, dacl string, protected bool) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("O:" + faUser(t) + "D:" + dacl)
	if err != nil {
		t.Fatalf("SDDL %q: %v", dacl, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	flags := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	if protected {
		flags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		flags |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, owner, nil, acl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo(%s, %q): %v", path, dacl, err)
	}
}

func faSecuritySnapshot(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}
	return sd.String()
}

func faJunction(t *testing.T, link, target string) {
	t.Helper()
	// Junctions need no symbolic-link privilege, so this runs as an ordinary user.
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v: %s", link, target, err, out)
	}
}

func faShortPath(t *testing.T, path string) string {
	t.Helper()
	long, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(long, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatalf("GetShortPathName(%s): %v", path, err)
	}
	return windows.UTF16ToString(buf[:n])
}

func TestParseLocalPathForms(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := parseLocalPath(wd)
	if err != nil {
		t.Fatalf("parse working directory %s: %v", wd, err)
	}
	for _, tc := range []struct{ in, want string }{
		{`C:\`, `C:\`},
		{`c:/Users/x`, `C:\Users\x`},
		{`C:\Users\\x\.\y\..\z`, `C:\Users\x\z`},
		{`\\?\C:\`, `C:\`},
		{`\\?\c:\Users\x`, `C:\Users\x`},
		{`rel\a`, cwd.String() + `\rel\a`},
		{`.\rel/b`, cwd.String() + `\rel\b`},
		{`a\..\b`, cwd.String() + `\b`},
		{`..\` + cwd.parts[len(cwd.parts)-1], cwd.String()},
		{`\\?\C:\Users\`, `C:\Users`},
		{`C:\name.ext\a b\x~1`, `C:\name.ext\a b\x~1`},
	} {
		got, err := parseLocalPath(tc.in)
		if err != nil || strings.TrimSuffix(got.String(), `\`) != strings.TrimSuffix(tc.want, `\`) {
			t.Errorf("parseLocalPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{
		``, "C:\\a\x00b",
		`\\server\share\x`, `//server/share/x`, `\/server\share`,
		`\\.\C:\x`, `\\.\PhysicalDrive0`, `\\?\UNC\server\share\x`, `\\?\GLOBALROOT\Device\HarddiskVolume1\x`,
		`\\?\Volume{00000000-0000-0000-0000-000000000000}\x`, `\??\C:\x`,
		`\\?\C:`, `\\?\C:/x`, `\\?\C:\a\..\b`, `\\?\C:\a\.\b`, `\\?\C:\a\\b`,
		`C:`, `C:x`, `C:..\x`, `\Users`, `/Users`, `1:\x`,
		`C:\a:b`, `C:\a\b::$DATA`, `C:\a\b:stream:$DATA`, `stream:x`,
		`C:\a.`, `C:\a `, `C:\a\b.\c`, `C:\a\...`, `\\?\C:\a.`, `\\?\C:\a `,
		`C:\CON`, `C:\a\nul.txt`, `C:\a\Aux .log`, `C:\COM1`, `C:\lpt9.x`, `C:\COM¹`, `C:\x\CONIN$`, `C:\x\conout$`,
		`C:\a<b`, `C:\a>b`, `C:\a|b`, `C:\a"b`, `C:\a?b`, `C:\a*b`, "C:\\a\tb",
		`C:\..`, `..\..\..\..\..\..\..\..\..\..\..\..\..\..\..\..\..\..\..\..`,
	} {
		if got, err := parseLocalPath(in); err == nil {
			t.Errorf("parseLocalPath(%q) = %q, want refusal", in, got)
		}
	}
}

func TestNormalizeNTStatus(t *testing.T) {
	for _, tc := range []struct {
		status windows.NTStatus
		want   error
	}{
		{windows.STATUS_OBJECT_NAME_NOT_FOUND, fs.ErrNotExist},
		{windows.STATUS_OBJECT_PATH_NOT_FOUND, fs.ErrNotExist},
		{windows.STATUS_OBJECT_NAME_COLLISION, fs.ErrExist},
		{windows.STATUS_ACCESS_DENIED, fs.ErrPermission},
	} {
		if err := normalizeNTStatus(tc.status); !errors.Is(err, tc.want) {
			t.Errorf("normalizeNTStatus(%v) = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestOpenDirectoryVolumeRoot(t *testing.T) {
	for _, path := range []string{`C:\`, `c:/`, `\\?\C:\`} {
		faSameFile(t, faOpenDir(t, path), `C:\`)
	}
}

func TestOpenDirectoryAliasesShareIdentity(t *testing.T) {
	dir := faMkdir(t, filepath.Join(faPrivateDir(t), "LongDirectoryNameForAlias"))
	parent := faOpenDir(t, dir)
	if err := faWriteFile(filepath.Join(dir, "LockFileWithLongName.lock"), []byte("alias"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := OpenRegularAt(parent, "LockFileWithLongName.lock", os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	want, err := PhysicalIdentity(lock)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(dir)
	t.Chdir(base) // Hosted checkout and TEMP may live on different volumes.
	aliases := map[string][2]string{
		"Extended":     {`\\?\` + dir, "LockFileWithLongName.lock"},
		"Case":         {base + `\LONGDIRECTORYNAMEFORALIAS`, "lockfilewithlongname.LOCK"},
		"ForwardSlash": {strings.ReplaceAll(dir, `\`, `/`), "LockFileWithLongName.lock"},
		"Relative":     {"", "LockFileWithLongName.lock"},
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Fatalf("relative fixture on the same volume: %v", err)
	}
	aliases["Relative"] = [2]string{rel, "LockFileWithLongName.lock"}
	for name, alias := range aliases {
		t.Run(name, func(t *testing.T) { faCheckAlias(t, alias[0], alias[1], dir, want) })
	}
	t.Run("ShortName", func(t *testing.T) {
		short := faShortPath(t, filepath.Join(dir, "LockFileWithLongName.lock"))
		if strings.EqualFold(filepath.Base(short), "LockFileWithLongName.lock") || strings.EqualFold(filepath.Base(filepath.Dir(short)), "LongDirectoryNameForAlias") {
			t.Skipf("8.3 short names are not generated on this volume (GetShortPathName returned %s)", short)
		}
		t.Logf("8.3 alias: %s", short)
		faCheckAlias(t, filepath.Dir(short), filepath.Base(short), dir, want)
	})
}

// faCheckAlias requires the same object and physical identity through aliases.
func faCheckAlias(t *testing.T, aliasDir, aliasName, dir, want string) {
	t.Helper()
	opened := faOpenDir(t, aliasDir)
	faSameFile(t, opened, dir)
	file, err := OpenRegularAt(opened, aliasName, os.O_RDONLY)
	if err != nil {
		t.Fatalf("OpenRegularAt(%s, %s): %v", aliasDir, aliasName, err)
	}
	defer file.Close()
	got, err := PhysicalIdentity(file)
	if err != nil || got != want {
		t.Fatalf("PhysicalIdentity via %s\\%s = %q, %v; want %q", aliasDir, aliasName, got, err, want)
	}
}

func TestCaseSensitiveDirectoryDoesNotFoldAliases(t *testing.T) {
	dir := faMkdir(t, filepath.Join(t.TempDir(), "sensitive"))
	if out, err := exec.Command("fsutil.exe", "file", "setCaseSensitiveInfo", dir, "enable").CombinedOutput(); err != nil {
		t.Skipf("per-directory case sensitivity is unavailable on this host: %v: %s", err, out)
	}
	faMkdir(t, filepath.Join(dir, "Child"))
	faSameFile(t, faOpenDir(t, dir+`\Child`), filepath.Join(dir, "Child"))
	// OBJ_CASE_INSENSITIVE must not fold a name the directory treats as distinct.
	if f, err := OpenDirectory(dir + `\CHILD`); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			f.Close()
		}
		t.Fatalf("OpenDirectory(case alias in case-sensitive dir) = %v, want not-exist", err)
	}
	if f, err := OpenDirectoryAt(faOpenDir(t, dir), "child"); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			f.Close()
		}
		t.Fatalf("OpenDirectoryAt(case alias in case-sensitive dir) = %v, want not-exist", err)
	}
}

func TestOpenRefusesJunctions(t *testing.T) {
	base := t.TempDir()
	target := faMkdir(t, filepath.Join(base, "target"))
	faMkdir(t, filepath.Join(target, "child"))
	if err := faWriteFile(filepath.Join(target, "file"), []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	faJunction(t, link, target)
	reparse := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "reparse point") {
			t.Errorf("%s = %v, want reparse-point refusal", name, err)
		}
	}
	for _, path := range []string{link, link + `\child`, `\\?\` + link + `\child`} {
		f, err := OpenDirectory(path)
		if err == nil {
			f.Close()
		}
		reparse("OpenDirectory("+path+")", err)
	}
	parent := faOpenDir(t, base)
	f, err := OpenDirectoryAt(parent, "junction")
	if err == nil {
		f.Close()
	}
	reparse("OpenDirectoryAt(junction)", err)
	for _, flags := range []int{os.O_RDONLY, os.O_RDWR} {
		if f, err := OpenRegularAt(parent, "junction", flags); err == nil {
			f.Close()
			t.Error("OpenRegularAt opened a junction")
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 2 {
		t.Fatalf("junction target changed: %v %v", entries, err)
	}
	opened := openNoFollow(t, link)
	for name, err := range map[string]error{
		"CheckPrivate":       checkPrivateHandle(opened, false),
		"CheckSafeDirectory": checkSafeDirectoryHandle(opened, false),
	} {
		if err == nil || !strings.Contains(err.Error(), "reparse point") {
			t.Errorf("%s(junction) = %v, want reparse refusal", name, err)
		}
	}
}

func TestOpenRegularAtRefusesSymbolicLinks(t *testing.T) {
	dir := t.TempDir()
	if err := faWriteFile(filepath.Join(dir, "target"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	faMkdir(t, filepath.Join(dir, "targetdir"))
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "filelink")); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("symbolic-link creation privilege is unavailable to this non-elevated session without Developer Mode: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "targetdir"), filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	parent := faOpenDir(t, dir)
	if f, err := OpenRegularAt(parent, "filelink", os.O_RDONLY); err == nil {
		f.Close()
		t.Error("OpenRegularAt opened a file symbolic link")
	}
	if f, err := OpenDirectoryAt(parent, "dirlink"); err == nil {
		f.Close()
		t.Error("OpenDirectoryAt opened a directory symbolic link")
	}
	if f, err := OpenDirectory(filepath.Join(dir, "dirlink")); err == nil {
		f.Close()
		t.Error("OpenDirectory followed a directory symbolic link")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "target")); string(data) != "t" {
		t.Fatalf("link target changed to %q", data)
	}
}

// Win32 would map these spellings onto existing objects or devices; the strict
// walk refuses them before any normalization.
func TestOpenRefusesWin32AliasesOfExistingObjects(t *testing.T) {
	dir := t.TempDir()
	if err := faWriteFile(filepath.Join(dir, "data"), []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := faWriteFile(filepath.Join(dir, "data:secret"), []byte("stream"), 0o600); err != nil {
		t.Fatalf("create alternate data stream: %v", err)
	}
	faMkdir(t, filepath.Join(dir, "sub"))
	for _, alias := range []string{"data.", "data ", "sub."} {
		if _, err := os.Stat(filepath.Join(dir, alias)); err != nil {
			t.Fatalf("Win32 alias %q does not resolve, so this test proves nothing: %v", alias, err)
		}
	}
	parent := faOpenDir(t, dir)
	for _, name := range []string{"data:secret", "data:secret:$DATA", "data::$DATA", "data.", "data ", "CON", "nul.txt", "COM1", "conin$"} {
		if f, err := OpenRegularAt(parent, name, os.O_RDONLY); err == nil {
			f.Close()
			t.Errorf("OpenRegularAt(%q) succeeded", name)
		}
	}
	for _, name := range []string{"sub.", "sub ", "sub::$INDEX_ALLOCATION", "AUX"} {
		if f, err := OpenDirectoryAt(parent, name); err == nil {
			f.Close()
			t.Errorf("OpenDirectoryAt(%q) succeeded", name)
		}
		if f, err := OpenDirectory(dir + `\` + name); err == nil {
			f.Close()
			t.Errorf("OpenDirectory(%q) succeeded", name)
		}
	}
	for _, path := range []string{`\\localhost\C$\Windows`, `\\.\C:\Windows`, `\\?\UNC\localhost\C$\Windows`, `\\?\GLOBALROOT\Device\Null`, `\??\C:\Windows`, `C:Windows`, `\Windows`} {
		if f, err := OpenDirectory(path); err == nil {
			f.Close()
			t.Errorf("OpenDirectory(%s) succeeded", path)
		}
	}
}

func TestPhysicalIdentityRefusesNonDiskObjects(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if id, err := PhysicalIdentity(read); err == nil {
		t.Fatalf("PhysicalIdentity(pipe) = %q", id)
	}
	base := t.TempDir()
	link := filepath.Join(base, "junction")
	faJunction(t, link, faMkdir(t, filepath.Join(base, "target")))
	// Go passes FILE_FLAG_OPEN_REPARSE_POINT through, opening the junction itself.
	junction, err := os.OpenFile(link, os.O_RDONLY|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer junction.Close()
	if id, err := PhysicalIdentity(junction); err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("PhysicalIdentity(junction) = %q, %v", id, err)
	}
}
