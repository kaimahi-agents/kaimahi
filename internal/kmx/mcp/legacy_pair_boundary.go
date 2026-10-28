package mcp

import (
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// Legacy MCP capabilities cannot execute or overwrite an agent-only view when
// its default tests source exists. CLI pair writers carry a private exact path;
// a later MCP pair capability must check both sources rather than bypass this.
func (f *Files) requireSingleSource(p string) error {
	if f.exact != "" {
		return nil
	}
	target, err := f.rootRelative(p)
	if err != nil {
		return err
	}
	if !f.allowedPath(target) {
		return refusal("unsafe_path")
	}
	physical := target
	if f.root != nil {
		physical = filepath.Join(f.root.Name(), filepath.FromSlash(target))
	}
	tests, ok := agentfile.DefaultTestsPath(physical)
	if !ok {
		return nil
	}
	if f.root != nil {
		relative, err := filepath.Rel(f.root.Name(), tests)
		if err != nil || !filepath.IsLocal(relative) {
			return refusal("paired_source_required")
		}
		_, err = f.root.Lstat(relative)
		if os.IsNotExist(err) {
			return nil
		}
	} else {
		_, err = os.Lstat(tests)
		if os.IsNotExist(err) {
			return nil
		}
	}
	return refusal("paired_source_required")
}
