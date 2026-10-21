package agentfile

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Load reads one bounded regular file without writing state or source. Static
// symlinks (including ancestors) and special files refuse before open. Like the
// workspace source reader, this is not protection against hostile concurrent
// filesystem replacement; root confinement belongs to the operation's caller.
func Load(filename string) (*Agent, error) {
	fail := func(problem string) (*Agent, error) { return nil, refuse(filename, 1, 1, "File", problem) }
	if filename == "" {
		return fail("select an explicit agent file")
	}
	// Abs cleans before we can inspect components. Refuse parent traversal
	// rather than erase a symlink followed by ".." and read a different file.
	path := filename[len(filepath.VolumeName(filename)):]
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fail("select an agent file path without parent traversal")
		}
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return fail("select a readable regular agent file")
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return fail("select an existing readable regular agent file")
		}
		// Windows mount-point reparse tags (directory junctions) are irregular,
		// not symlinks, in current Go. Refuse either before following ancestors.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fail("select a file with no symlinks or reparse points in its path")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	before, err := os.Lstat(absolute)
	if err != nil || !before.Mode().IsRegular() || before.Size() > MaxFileBytes {
		return fail("select a regular agent file of at most 256 KiB")
	}
	f, err := os.Open(absolute)
	if err != nil {
		return fail("select a readable regular agent file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() {
		return fail("agent file changed while opening; select a stable regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes || int64(len(data)) != opened.Size() {
		return fail("agent file changed or exceeds 256 KiB; select a stable bounded file")
	}
	return Parse(filename, data)
}
