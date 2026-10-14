package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
)

// Discover selects the nearest workspace.yaml ancestor. An explicit root never
// falls back to an ancestor; malformed, unreadable or linked markers stop search.
func Discover(start, override string) (string, error) {
	return Resolve(start, override)
}

type Summary struct{ Name, Member, Execution string }

func Describe(root string) (Summary, error) {
	resolved, err := Discover(root, root)
	if err != nil {
		return Summary{}, err
	}
	root = resolved
	r, err := os.OpenRoot(root)
	if err != nil {
		return Summary{}, err
	}
	defer r.Close()
	b, err := readBounded(r, "workspace.yaml")
	if err != nil {
		return Summary{}, err
	}
	var w sourceWorkspace
	if err := strictYAML(b, &w); err != nil {
		return Summary{}, err
	}
	b, err = readBounded(r, w.Member)
	if err != nil {
		return Summary{}, err
	}
	var m sourceMember
	if err := strictYAML(b, &m); err != nil {
		return Summary{}, err
	}
	mode := m.Execution
	if mode == "" {
		mode = NativeExecution
		if profile, err := m.Runtime.profile(); err != nil {
			return Summary{}, err
		} else if profile.ID == "kagent" {
			mode = "native-kagent"
		}
	}
	if mode != NativeExecution && mode != "native-kagent" && mode != ImageExecution {
		return Summary{}, errors.New("unsupported workspace execution profile")
	}
	return Summary{w.Name, m.ID, mode}, nil
}

// ValidateImageDirectory validates the complete closed image execution profile,
// including packaged case coverage. It never consults authored workspace files.
func ValidateImageDirectory(ctx context.Context, root string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	files := map[string][]byte{}
	total := 0
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if len(files) >= 135 {
			return errors.New("image suite exceeds file limit")
		}
		b, err := readBounded(r, name)
		if err != nil {
			return err
		}
		total += len(b)
		if total > 8<<20 {
			return errors.New("image suite exceeds size limit")
		}
		files[name] = b
		return nil
	})
	if err != nil {
		return err
	}
	if files[imagePath] == nil {
		return errors.New("release does not declare image-http execution; native release lift is a separate adapter")
	}
	_, err = validateNative(files)
	if err != nil {
		return errors.New("invalid packaged image execution profile")
	}
	return nil
}
