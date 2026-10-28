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
	// A legacy root at .github/agents has its default sibling outside the root.
	// Probe only that derived name's presence; never read it or expand write access.
	stat := os.Lstat
	if f.root != nil {
		relative, err := filepath.Rel(f.root.Name(), tests)
		if err == nil && filepath.IsLocal(relative) {
			tests, stat = relative, f.root.Lstat
		}
	}
	if _, err := stat(tests); os.IsNotExist(err) {
		return nil
	}
	return refusal("paired_source_required")
}
