package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// ErrNotFound distinguishes an absent workspace from invalid or unreadable state.
var ErrNotFound = errors.New("workspace not found")

// Resolve returns the absolute root selected by an exact override, or by the
// nearest ancestor workspace.yaml starting at start. Relative overrides use
// start; absolute overrides do not depend on it. Resolution reads only the
// bounded descriptor, leaving member/case validation and caching to Prepare.
func Resolve(start, override string) (string, error) {
	selected := start
	if override != "" {
		if err := safeText(override); err != nil {
			return "", err
		}
		selected = override
		if !filepath.IsAbs(override) {
			base, err := safeAbsolute(start)
			if err != nil {
				return "", err
			}
			selected = filepath.Join(base, override)
		}
	}
	root, err := safeAbsolute(selected)
	if err != nil {
		return "", err
	}
	// Relative input may inherit sensitive spelling from the invocation cwd.
	if err := safeText(root); err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspace start/override must be a directory")
	}
	for {
		if err := resolveMarker(root); err == nil {
			return root, nil
		} else if !errors.Is(err, ErrNotFound) {
			return "", err
		}
		parent := filepath.Dir(root)
		if override != "" || parent == root {
			return "", ErrNotFound
		}
		root = parent
	}
}

func resolveMarker(root string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	info, err := r.Lstat("workspace.yaml")
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace symlinks are not supported")
	}
	// Check before opening: a FIFO or device must not block discovery.
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return errors.New("workspace descriptor must be a regular file of at most 1 MiB")
	}
	b, err := readBounded(r, "workspace.yaml")
	if err != nil {
		return err
	}
	_, err = parseSourceWorkspace(b)
	return err
}

func parseSourceWorkspace(b []byte) (sourceWorkspace, error) {
	var w sourceWorkspace
	if err := strictYAML(b, &w); err != nil {
		// YAML diagnostics can quote decoded credential-shaped keys/content.
		return w, errors.New("workspace.yaml: invalid workspace descriptor; check source fields and integer schemaVersion")
	}
	if w.SchemaVersion != 1 || !namePattern.MatchString(w.Name) || w.Name == "." || w.Name == ".." || w.BuildProfile != "build-profiles/default.json" {
		return w, errors.New("unsupported workspace schema/name/build profile")
	}
	parts := strings.Split(w.Member, "/")
	if len(parts) != 3 || parts[0] != "agents" || parts[2] != "member.yaml" {
		return w, errors.New("workspace member path must select agents/ID/member.yaml")
	}
	if err := scaffold.ValidateName(parts[1]); err != nil {
		return w, fmt.Errorf("workspace member path: %w", err)
	}
	return w, nil
}
