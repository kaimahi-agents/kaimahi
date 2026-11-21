package workspacetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	suiteoci "github.com/agent-primitives/suite/oci"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

var errGeneratedValidation = errors.New("generated workspace package validation unavailable")

type generatedTree struct {
	directories map[string]*fileaccess.Directory
	files       []string
	uncertain   bool
}

// stateGeneratedValidator validates one generated loose AgentSuite view below
// qualified private state, then removes only its retained identity-bound files
// and empty directories before returning.
func stateGeneratedValidator(root string) func(context.Context, map[string][]byte) error {
	return func(ctx context.Context, files map[string][]byte) (err error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		parent, err := fileaccess.EnsurePrivateDirectory(ctx, root)
		if err != nil {
			return errGeneratedValidation
		}
		defer func() { err = errors.Join(err, parent.Close()) }()
		lock, err := fileaccess.EnsurePrivateFileAt(ctx, parent, "lock")
		if err != nil {
			return errGeneratedValidation
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
		info, statErr := lock.Stat()
		links, linksErr := fileaccess.LinkCount(ctx, lock)
		locked, lockErr := fileaccess.TryLockOutcome(ctx, lock)
		if statErr != nil || linksErr != nil || lockErr != nil || !locked || info.Size() != 0 || links != 1 {
			return errGeneratedValidation
		}
		entries, readErr := parent.ReadDir(3)
		if readErr != nil || len(entries) != 1 || entries[0].Name() != "lock" {
			return errGeneratedValidation
		}
		stageName, err := randomValidationName()
		if err != nil {
			return errGeneratedValidation
		}
		stage, err := fileaccess.CreatePrivateDirectoryAt(ctx, parent, stageName)
		if err != nil {
			return errGeneratedValidation
		}
		tree := &generatedTree{directories: map[string]*fileaccess.Directory{".": stage}}
		defer func() {
			if cleanupErr := tree.remove(context.WithoutCancel(ctx), parent, stageName); cleanupErr != nil {
				err = errors.Join(err, errGeneratedValidation)
			}
		}()
		if err := tree.write(ctx, files); err != nil {
			return err
		}
		if _, err := suiteoci.ValidateDirectory(filepath.Join(root, stageName)); err != nil {
			return errGeneratedValidation
		}
		return ctx.Err()
	}
}

func (t *generatedTree) write(ctx context.Context, files map[string][]byte) error {
	for _, name := range generatedDirectories(files) {
		parent := t.directories[path.Dir(name)]
		if parent == nil {
			return errGeneratedValidation
		}
		directory, err := fileaccess.CreatePrivateDirectoryAt(ctx, parent, path.Base(name))
		if err != nil {
			return errGeneratedValidation
		}
		t.directories[name] = directory
	}
	for _, name := range sortedGeneratedFiles(files) {
		if !validGeneratedPath(name) {
			return errGeneratedValidation
		}
		parent := t.directories[path.Dir(name)]
		if parent == nil {
			return errGeneratedValidation
		}
		result, err := fileaccess.PublishPrivate(ctx, parent, path.Base(name), files[name])
		if result.Visible {
			t.files = append(t.files, name)
		}
		if err != nil {
			if !result.VisibilityKnown {
				t.uncertain = true
			}
			return errGeneratedValidation
		}
	}
	return nil
}

func (t *generatedTree) remove(ctx context.Context, parent *fileaccess.Directory, stageName string) (err error) {
	if t == nil {
		return errGeneratedValidation
	}
	defer func() { err = errors.Join(err, t.closeDirectories()) }()
	if t.directories["."] == nil || t.uncertain {
		return errGeneratedValidation
	}
	for _, name := range t.files {
		directory := t.directories[path.Dir(name)]
		file, err := fileaccess.OpenPrivateFileAt(ctx, directory, path.Base(name))
		if err != nil {
			return errGeneratedValidation
		}
		removeErr := fileaccess.RemovePrivateFileAt(ctx, directory, path.Base(name), file)
		closeErr := file.Close()
		if removeErr != nil || closeErr != nil {
			return errGeneratedValidation
		}
	}
	directories := generatedDirectoryNames(t.directories)
	for i := len(directories) - 1; i >= 0; i-- {
		name := directories[i]
		directory := t.directories[name]
		ancestor := t.directories[path.Dir(name)]
		if err := fileaccess.RemovePrivateDirectoryAt(ctx, ancestor, path.Base(name), directory); err != nil {
			return errGeneratedValidation
		}
		if err := directory.Close(); err != nil {
			return errGeneratedValidation
		}
		delete(t.directories, name)
	}
	stage := t.directories["."]
	if err := fileaccess.RemovePrivateDirectoryAt(ctx, parent, stageName, stage); err != nil {
		return errGeneratedValidation
	}
	if err := stage.Close(); err != nil {
		return errGeneratedValidation
	}
	delete(t.directories, ".")
	return nil
}

func (t *generatedTree) closeDirectories() error {
	var err error
	for name, directory := range t.directories {
		if directory != nil {
			err = errors.Join(err, directory.Close())
		}
		delete(t.directories, name)
	}
	return err
}

func generatedDirectories(files map[string][]byte) []string {
	set := map[string]bool{}
	for name := range files {
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			set[directory] = true
		}
	}
	directories := make([]string, 0, len(set))
	for name := range set {
		directories = append(directories, name)
	}
	sort.Slice(directories, func(i, j int) bool {
		depthI, depthJ := strings.Count(directories[i], "/"), strings.Count(directories[j], "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		return directories[i] < directories[j]
	})
	return directories
}

func generatedDirectoryNames(directories map[string]*fileaccess.Directory) []string {
	names := make([]string, 0, len(directories)-1)
	for name := range directories {
		if name != "." {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		depthI, depthJ := strings.Count(names[i], "/"), strings.Count(names[j], "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		return names[i] < names[j]
	})
	return names
}

func sortedGeneratedFiles(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func randomValidationName() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return ".workspace-test-" + hex.EncodeToString(raw), nil
}

func validGeneratedPath(name string) bool {
	return name != "" && fs.ValidPath(name) && path.Clean(name) == name
}
