package mcp

import (
	"os"
	"path"
)

// sourceIdentity is operation-owned, never a client-supplied revision capability.
// It strengthens cooperative publication without excluding hostile outside writers.
type sourceIdentity struct{ file, parent os.FileInfo }

func squadEnrichmentError(cause error) error {
	if e, ok := cause.(*Error); ok {
		switch e.Code {
		case "cancelled", "deadline_exceeded":
			return cause
		case "stale_revision":
			return refusal("squad_starter_changed")
		}
	}
	return refusal("squad_enrichment_failed")
}

func (f *Files) observedParent(p string, parent *os.Root, observed *sourceIdentity) bool {
	openedRoot, err := f.root.Stat(".")
	if err != nil {
		return false
	}
	namedRoot, err := os.Lstat(f.root.Name())
	if err != nil || !namedRoot.IsDir() || !os.SameFile(openedRoot, namedRoot) {
		return false
	}
	pinned, err := parent.Stat(".")
	if err != nil || !os.SameFile(observed.parent, pinned) {
		return false
	}
	current, err := f.root.Lstat(path.Dir(p))
	return err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(current, pinned)
}
