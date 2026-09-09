package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteNew writes the manifest with an EXCLUSIVE create.
//
// The file is the artifact and the operator owns it: once they have edited
// it, a second `kmx agent create` with the same name must not silently
// replace their work. There is no --force; the remedy is to name a different
// file, or to delete the one you have decided you do not want.
func WriteNew(path, content string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists — refusing to overwrite it.\n"+
				"  It may be a manifest you have edited. Apply it as it stands:\n"+
				"    kubectl apply -f %s\n"+
				"  or scaffold under another name with --out <path>.", path, path)
		}
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	return nil
}

// WriteNewOrIdentical is WriteNew for a command that is meant to be
// re-runnable: an existing file whose bytes are exactly what would have
// been written is left alone and reported as unchanged, and anything else
// keeps WriteNew's refusal.
//
// The distinction is the operator's edits, not the write. A file this
// command would have produced verbatim carries no work to lose, so
// refusing it would only stand between an operator and a re-run that
// changes nothing on disk — while a file that differs may be theirs, and
// is still refused.
func WriteNewOrIdentical(path, content string) (unchanged bool, err error) {
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if string(existing) == content {
			return true, nil
		}
	}
	return false, WriteNew(path, content)
}
