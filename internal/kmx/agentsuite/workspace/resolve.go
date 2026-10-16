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
	// Pointers distinguish absent fields from empty mixed-form declarations.
	var descriptor struct {
		SchemaVersion int              `yaml:"schemaVersion"`
		Name          string           `yaml:"name"`
		Member        *string          `yaml:"member"`
		Members       *[]string        `yaml:"members"`
		BuildProfile  *string          `yaml:"buildProfile"`
		Profiles      *[]sourceProfile `yaml:"profiles"`
	}
	var w sourceWorkspace
	if err := strictSourceYAML(b, &descriptor, true); err != nil {
		// YAML diagnostics can quote decoded credential-shaped keys/content.
		return w, errors.New("workspace.yaml: invalid workspace descriptor; check source fields and integer schemaVersion")
	}
	w.SchemaVersion, w.Name = descriptor.SchemaVersion, descriptor.Name
	if descriptor.BuildProfile != nil {
		w.BuildProfile = *descriptor.BuildProfile
	}
	if !namePattern.MatchString(w.Name) || w.Name == "." || w.Name == ".." {
		return w, errors.New("unsupported workspace schema/name/build profile")
	}
	if w.SchemaVersion != 3 && (w.BuildProfile != "build-profiles/default.json" || descriptor.Profiles != nil) {
		return w, errors.New("legacy workspace requires the default build profile and forbids profiles")
	}
	var members []string
	switch w.SchemaVersion {
	case 3:
		if descriptor.Member == nil || descriptor.Members != nil || descriptor.BuildProfile != nil || descriptor.Profiles == nil || len(*descriptor.Profiles) == 0 || len(*descriptor.Profiles) > 16 {
			return w, errors.New("schemaVersion 3 requires member and one to 16 profiles; forbids members and buildProfile")
		}
		w.Member, w.Profiles = *descriptor.Member, *descriptor.Profiles
		members = []string{w.Member}
		for _, row := range w.Profiles {
			if row.RuntimeProfile == "" || row.BuildProfile == "" || row.Platform == "" {
				return w, errors.New("profile rows require runtimeProfile, buildProfile and platform")
			}
		}
	case 1:
		if descriptor.Member == nil || descriptor.Members != nil {
			return w, errors.New("schemaVersion 1 requires member and forbids members")
		}
		w.Member = *descriptor.Member
		members = []string{w.Member}
	case 2:
		if descriptor.Member != nil || descriptor.Members == nil || len(*descriptor.Members) < 2 || len(*descriptor.Members) > maxMembers {
			return w, errors.New("unsupported workspace: schemaVersion 2 requires two to 16 members and forbids member")
		}
		w.Members = *descriptor.Members
		members = w.Members
	default:
		return w, errors.New("unsupported workspace schema/name/build profile")
	}
	seen := map[string]bool{}
	for _, member := range members {
		parts := strings.Split(member, "/")
		if len(parts) != 3 || parts[0] != "agents" || parts[2] != "member.yaml" {
			return w, errors.New("workspace member path must select agents/ID/member.yaml")
		}
		if err := scaffold.ValidateName(parts[1]); err != nil {
			return w, fmt.Errorf("workspace member path: %w", err)
		}
		if seen[member] {
			return w, errors.New("duplicate workspace member")
		}
		seen[member] = true
	}
	return w, nil
}

// RequireSingleMember is a bounded, read-only descriptor preflight for operations
// that cannot support multi-member workspaces. It does not inspect member files.
func RequireSingleMember(root string) error {
	root, err := safeAbsolute(root)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := readBounded(r, "workspace.yaml")
	if err != nil {
		return err
	}
	w, err := parseSourceWorkspace(b)
	if err != nil {
		return err
	}
	if w.SchemaVersion == 2 {
		return ErrMultiMemberUnsupported
	}
	return nil
}
