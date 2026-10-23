package mcp

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	kmruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// OpenSquadSetup enables conditional setup, but performs no child action. The
// caller must obtain approval of the complete plan and native setup effects.
// Automatic setup supports separately qualified standalone launchers only.
func OpenSquadSetup(root string, program SquadProgram) (*SquadCreator, error) {
	c, err := openSquad(root, program, true)
	if err != nil {
		return nil, err
	}
	name, expected := filepath.Base(c.program), "squad"
	if program.GOOS == "windows" {
		name = strings.ToLower(name)
		expected = "squad.exe"
	}
	if name != expected {
		c.Close()
		return nil, refusal("unsupported_squad_program")
	}
	return c, nil
}
func (c *SquadCreator) setupMissing() bool {
	_, err := c.root.Lstat(".squad")
	return os.IsNotExist(err)
}
func (c *SquadCreator) checkProgram() error {
	info, err := os.Stat(c.program)
	if err != nil || !os.SameFile(c.programInfo, info) || info.Size() != c.programInfo.Size() || !info.ModTime().Equal(c.programInfo.ModTime()) {
		return refusal("unsupported_squad_program")
	}
	return nil
}
func (c *SquadCreator) checkSetupRoot() error {
	if err := c.checkSelectedRoot(); err != nil {
		return err
	}
	// These native setup targets cannot be adopted, merged or silently replaced.
	for _, p := range []string{".squad", ".mcp.json", ".copilot", ".github/skills", ".github/agents/squad.agent.md"} {
		if _, err := c.root.Lstat(p); !os.IsNotExist(err) {
			return refusal("unsafe_squad_root")
		}
	}
	for _, p := range []string{".git", ".github", ".github/agents"} {
		info, err := c.root.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return refusal("unsafe_squad_root")
		}
	}
	for _, p := range []string{".gitignore", ".gitattributes"} {
		if _, err := c.root.Lstat(p); os.IsNotExist(err) {
			continue
		}
		if _, err := c.readLocal(p); err != nil {
			return refusal("unsafe_squad_root")
		}
	}
	return nil
}

// Git command-scope overrides prevent inherited/global configuration and origin
// introspection from redirecting setup or probing a remote platform. No shell
// or Git command is constructed by kmx. HOME remains the operator's; the native
// dependency is trusted ambient execution, not an OS sandbox.
func (c *SquadCreator) setupEnvironment() []string {
	return []string{
		"SQUAD_STANDALONE_HOME=" + filepath.Dir(c.program),
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(c.root.Name()), "GIT_WORK_TREE=" + c.root.Name(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=remote.origin.url", "GIT_CONFIG_VALUE_0=",
	}
}
func (c *SquadCreator) checkSetupMCP() error {
	b, err := c.readLocal(".mcp.json")
	if err != nil || !kmruntime.JSONKeysUnique(b) {
		return refusal("unsafe_squad_root")
	}
	var doc struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return refusal("unsafe_squad_root")
	}
	m := doc.Servers["squad_state"]
	if (m.Command != "squad" && m.Command != "squad.exe" && m.Command != c.program) || len(m.Args) != 1 || m.Args[0] != "state-mcp" {
		return refusal("unsafe_squad_root")
	}
	return nil
}

// setupSnapshot covers retained native setup/cast files, not every transient
// write or arbitrary dependency effect. Outside writers must respect quiet time.
func (c *SquadCreator) setupSnapshot() (map[string]string, error) {
	if err := c.checkSelectedRoot(); err != nil {
		return nil, err
	}
	for _, p := range []string{".github", ".github/agents"} {
		info, err := c.root.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, refusal("unsafe_squad_root")
		}
	}
	seen := map[string]string{}
	total, count := 0, 0
	for _, base := range []string{".squad", ".github/skills", ".copilot", ".github/agents/squad.agent.md", ".mcp.json", ".gitignore", ".gitattributes"} {
		if _, err := c.root.Lstat(base); os.IsNotExist(err) {
			continue
		}
		err := fs.WalkDir(c.root.FS(), base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return refusal("unsafe_squad_root")
			}
			count++
			if count > 4096 {
				return refusal("unsafe_squad_root")
			}
			info, err := d.Info()
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return refusal("unsafe_squad_root")
			}
			if info.IsDir() {
				seen[p] = "directory"
				return nil
			}
			b, err := c.readLocal(p)
			if err != nil {
				return err
			}
			total += len(b)
			if total > squadTreeBudget {
				return refusal("unsafe_squad_root")
			}
			seen[p] = fmt.Sprint(info.Mode().Perm()) + ":" + revision(b)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return seen, nil
}
