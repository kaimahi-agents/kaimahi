package blueprint

// Where a blueprint lives, and why that is a decision rather than a default.
//
// Three candidates were on the table, and they have different failure
// modes:
//
//  1. CARRIED BY THE BINARY, by name — `kmx workflow govern release`.
//     Chosen as the default. The front door is `curl | sh` then
//     `kmx quickstart`, with no Go and no checkout; a blueprint that
//     could only be read out of a git clone would make `git clone` a
//     prerequisite again, for the one feature whose whole point is that
//     expressing a workflow should be cheap. The blueprints kmx carries
//     are reviewed in this repository and shipped with the release that
//     was built from it, so `kmx --version` identifies them exactly.
//
//  2. A PATH — `--file ./my-release.yaml`. Chosen as the second form,
//     because a blueprint an operator wrote is theirs and belongs beside
//     their own configuration. A local file is reviewable before it is
//     used and its provenance is whatever the operator's own tooling
//     says it is.
//
//  3. A FETCHED URL. REJECTED. A blueprint decides which tools a
//     credential may call and what its standing bounds are, so fetching
//     one is letting a remote party set policy. The toolchain precedent
//     (internal/kmx/toolchain) does not transfer: what makes a fetched
//     `kubectl` safe is a PINNED VERSION and a CHECKSUM, and a blueprint
//     URL that an operator wants to keep current is by construction
//     mutable. A pinned, checksummed blueprint URL is `curl -o` followed
//     by `--file`, which keeps the review step visible instead of hiding
//     it inside a flag. If this is ever revisited it needs a signing
//     story, not a download.
//
// The operator's CONFIGURATION — which repository, which organization,
// which project — is never in the blueprint under any of the three. It is
// `--set`, or a values file, for the reason scripts/release-bind.sh
// states: somebody's real project is not a thing a public repository
// commits.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Bundle is a parsed blueprint plus the filesystem its scripts come from.
type Bundle struct {
	*Blueprint
	// Source says where it came from, for `kmx workflow show` and for
	// the guard prompt. An operator about to let a file decide what
	// their credential may call should be told which file.
	Source string
	// scripts resolves a bundled script name to its bytes.
	scripts func(name string) ([]byte, error)
}

// Carried lists the blueprints a kmx binary carries.
func Carried(carried fs.FS) ([]*Bundle, error) {
	entries, err := fs.ReadDir(carried, "blueprints")
	if err != nil {
		return nil, err
	}
	var out []*Bundle
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".yaml")
		if e.IsDir() || name == e.Name() {
			continue
		}
		b, err := Load(carried, name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load reads one blueprint the binary carries.
func Load(carried fs.FS, name string) (*Bundle, error) {
	if !dnsLabelRE.MatchString(name) {
		return nil, fmt.Errorf("%q is not a blueprint name", name)
	}
	raw, err := fs.ReadFile(carried, "blueprints/"+name+".yaml")
	if err != nil {
		names, _ := carriedNames(carried)
		return nil, fmt.Errorf("kmx carries no blueprint %q. It carries: %s.\n"+
			"  A blueprint you wrote is a file: kmx workflow … --file ./%s.yaml",
			name, strings.Join(names, ", "), name)
	}
	b, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if b.Name != name {
		return nil, fmt.Errorf("blueprints/%s.yaml declares `name: %s`; the file name and the name have to "+
			"agree, because one of them is what an operator types", name, b.Name)
	}
	bundle := &Bundle{Blueprint: b, Source: "carried by kmx (blueprints/" + name + ".yaml)"}
	bundle.scripts = func(script string) ([]byte, error) {
		return fs.ReadFile(carried, "scripts/"+script)
	}
	return bundle, bundle.checkScripts()
}

// LoadFile reads a blueprint an operator wrote. Its scripts are resolved
// beside it — never from anywhere else, and never by a path the file
// itself supplies, which is why `script:` is a bare file name.
func LoadFile(path string) (*Bundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	bundle := &Bundle{Blueprint: b, Source: path}
	bundle.scripts = func(script string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, script))
	}
	return bundle, bundle.checkScripts()
}

// Script returns a bundled script's bytes.
func (b *Bundle) Script(name string) ([]byte, error) {
	if !scriptNameRE.MatchString(name) {
		return nil, fmt.Errorf("%q is not a bundled script name", name)
	}
	return b.scripts(name)
}

// checkScripts refuses a blueprint whose scripts are not there, at LOAD
// rather than at the step. A release that stops three approvals in
// because a file is missing has already spent somebody's attention.
func (b *Bundle) checkScripts() error {
	for _, name := range b.Scripts() {
		if _, err := b.Script(name); err != nil {
			return fmt.Errorf("blueprint %q runs the script %q, and it is not in the bundle: %w.\n"+
				"  A bundled script sits beside the blueprint file", b.Name, name, err)
		}
	}
	return nil
}

func carriedNames(carried fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(carried, "blueprints")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name := strings.TrimSuffix(e.Name(), ".yaml"); name != e.Name() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
