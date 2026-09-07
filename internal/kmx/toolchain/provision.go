package toolchain

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Source says where a provisioned tool came from.
type Source string

const (
	// FromPath is the operator's own copy. It always wins: a machine that
	// already has kubectl is a machine whose kubectl is the one its owner
	// expects `kmx` to use, and quietly shadowing it with a pinned build
	// would be a surprise nobody asked for.
	FromPath Source = "PATH"
	// FromCache is a previously fetched copy that was re-verified against
	// its recorded digest and kept. It is reported by the fetch itself, so
	// it means the run really did use those bytes — an entry that existed
	// but failed re-verification is deleted and re-fetched, and reports
	// FromDownload. That distinction is the point: the tampered case is
	// the one an operator most needs named, and deciding the label from a
	// Stat beforehand is what used to hide it.
	FromCache Source = "cache"
	// FromDownload is a copy fetched during this run.
	FromDownload Source = "downloaded"
)

// Tool is one provisioned dependency.
type Tool struct {
	Name    string
	Path    string
	Version string
	Source  Source
}

// Provision makes each named tool runnable and reports where each came from.
//
// linkDir gets a plain-named symlink for every tool that was NOT already on
// PATH, and the caller prepends it to PATH. The cache itself cannot be used
// for that: its entries carry the version in the name (`kind-0.33.0-linux-amd64`)
// so that a pin bump can never be served the previous binary, and nothing
// looks for a command under that name.
//
// Absent from this list, deliberately: the container engine. Docker and
// Podman are a daemon, a system package and usually a privileged install —
// not a binary that can be dropped into a cache directory — so a container
// engine remains the one thing an operator installs themselves, and the
// preflight still says so plainly when it is missing.
func Provision(names []string, linkDir string, opt Options) ([]Tool, error) {
	goos, goarch := Platform()
	var tools []Tool
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			tools = append(tools, Tool{Name: name, Path: path, Source: FromPath})
			continue
		}
		spec, ok := Pinned(name, goos, goarch)
		if !ok {
			return nil, fmt.Errorf("kmx has no pinned download for %q — install it and put it on PATH", name)
		}
		// The source comes back FROM the fetch, not from a Stat before it:
		// an entry that exists may still be re-fetched, because one whose
		// recorded digest no longer matches is deleted rather than run.
		path, source, err := EnsureReporting(spec, opt)
		if err != nil {
			return nil, err
		}
		link := filepath.Join(linkDir, name)
		if err := relink(link, path); err != nil {
			return nil, err
		}
		tools = append(tools, Tool{Name: name, Path: link, Version: spec.Version, Source: source})
	}
	return tools, nil
}

// relink points linkDir/<name> at the versioned cache entry, replacing
// whatever was there. Replacing rather than keeping is the point: after a pin
// bump the old link would otherwise still be the thing on PATH.
func relink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, link)
}

// PrependPath puts dir at the front of a PATH value. It is a no-op when dir
// is already leading, so running it twice cannot grow the variable.
func PrependPath(path, dir string) string {
	if dir == "" {
		return path
	}
	if path == "" {
		return dir
	}
	if strings.HasPrefix(path, dir+string(os.PathListSeparator)) || path == dir {
		return path
	}
	return dir + string(os.PathListSeparator) + path
}

// Report writes one line per tool, so it is visible which of them came from
// the machine and which kmx put there.
func Report(w io.Writer, tools []Tool) {
	if w == nil {
		return
	}
	for _, t := range tools {
		switch t.Source {
		case FromPath:
			fmt.Fprintf(w, "  %-8s %s (yours, already on PATH)\n", t.Name, t.Path)
		default:
			fmt.Fprintf(w, "  %-8s v%s (%s by kmx, checksum-verified)\n", t.Name, t.Version, t.Source)
		}
	}
}
