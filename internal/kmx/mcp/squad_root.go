package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	kmruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const squadTreeBudget = 8 << 20

func (c *SquadCreator) readLocal(p string) ([]byte, error) {
	info, err := c.root.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > squadTreeBudget {
		return nil, refusal("unsafe_squad_root")
	}
	f, err := openTarget(c.root, p)
	if err != nil {
		return nil, refusal("unsafe_squad_root")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || !singleLink(f) {
		return nil, refusal("unsafe_squad_root")
	}
	b, err := io.ReadAll(io.LimitReader(f, squadTreeBudget+1))
	if err != nil || len(b) > squadTreeBudget {
		return nil, refusal("unsafe_squad_root")
	}
	return b, nil
}

func (c *SquadCreator) checkRoot() error {
	bad := func() error { return refusal("unsafe_squad_root") }
	pinned, err := c.root.Stat(".")
	if err != nil {
		return bad()
	}
	current, err := os.Lstat(c.root.Name())
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(pinned, current) {
		return bad()
	}
	resolved, err := filepath.EvalSymlinks(c.root.Name())
	if err != nil || resolved != c.root.Name() {
		return bad()
	}
	// The qualified default init has no executable TS configuration. Do not
	// guess identities from arbitrary TS expressions/template literals.
	for _, p := range []string{".ai-team", "squad.config.ts"} {
		if _, err := c.root.Lstat(p); !os.IsNotExist(err) {
			return bad()
		}
	}
	dir, err := c.root.Lstat(".squad")
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return bad()
	}
	b, err := c.readLocal(".squad/config.json")
	if err != nil {
		return bad()
	}
	// Native JSON.parse is case-sensitive. A Go struct decoder would accept
	// TeamRoot as an alias and could hide a real lowercase external redirect.
	var config map[string]json.RawMessage
	if !kmruntime.JSONKeysUnique(b) || json.Unmarshal(b, &config) != nil {
		return bad()
	}
	for key := range config {
		if key != "version" && key != "teamRoot" {
			return bad()
		}
	}
	var version int
	if json.Unmarshal(config["version"], &version) != nil || version != 1 {
		return bad()
	}
	if value, present := config["teamRoot"]; present {
		var target string
		if json.Unmarshal(value, &target) != nil || target != "." {
			return bad()
		}
	}
	for _, p := range []string{".squad/team.md", ".squad/routing.md"} {
		if _, err := c.readLocal(p); err != nil {
			return bad()
		}
	}
	return nil
}

// snapshot observes retained files, not every transient write performed by the
// native process. It rejects static link/special-file hazards before invoking it.
func (c *SquadCreator) snapshot() (map[string]string, error) {
	seen := map[string]string{}
	total, count := 0, 0
	err := fs.WalkDir(c.root.FS(), ".squad", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return refusal("unsafe_squad_root")
		}
		count++
		if count > 4096 {
			return refusal("unsafe_squad_root")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return refusal("unsafe_squad_root")
		}
		if info.IsDir() {
			seen[p] = "directory"
			return nil
		}
		b, err := c.readLocal(p)
		if err != nil {
			return refusal("unsafe_squad_root")
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
	// Cast may update these native configuration/capability files too. Reject
	// parent aliases even when the capability leaf itself has not been created.
	for _, p := range []string{".github", ".github/agents"} {
		info, err := c.root.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, refusal("unsafe_squad_root")
		}
	}
	for _, p := range []string{"squad.config.ts", ".github/agents/squad.agent.md"} {
		if _, err := c.root.Lstat(p); os.IsNotExist(err) {
			continue
		}
		b, err := c.readLocal(p)
		if err != nil {
			return nil, refusal("unsafe_squad_root")
		}
		total += len(b)
		if total > squadTreeBudget {
			return nil, refusal("unsafe_squad_root")
		}
		seen[p] = revision(b)
	}
	return seen, nil
}

func (c *SquadCreator) preflightMember(m SquadMember, seen map[string]string) error {
	for p := range seen {
		if p == ".squad/casting/registry.lock" || strings.HasPrefix(p, ".squad/casting/registry.lock.") || strings.HasPrefix(p, ".squad/casting/registry-history.transaction") || (strings.HasPrefix(p, ".squad/casting/") && strings.Contains(p, ".tmp-")) {
			return refusal("squad_recovery_required")
		}
	}
	if _, err := c.root.Lstat(".squad/agents/" + m.ID); !os.IsNotExist(err) {
		return refusal("already_exists")
	}
	team, err := c.readLocal(".squad/team.md")
	if err != nil {
		return refusal("unsafe_squad_root")
	}
	routes, err := c.readLocal(".squad/routing.md")
	if err != nil {
		return refusal("unsafe_squad_root")
	}
	if bytes.Contains(team, []byte(".squad/agents/"+m.ID+"/charter.md")) || bytes.Contains(routes, []byte("| "+m.ID+" |")) {
		return refusal("already_exists")
	}
	agents, err := c.registry()
	if err != nil {
		return err
	}
	if _, exists := agents[m.ID]; exists {
		return refusal("already_exists")
	}
	if b, err := c.readLocal("squad.config.ts"); err == nil && (bytes.Contains(b, []byte("'"+m.ID+"'")) || bytes.Contains(b, []byte("\""+m.ID+"\""))) {
		return refusal("already_exists")
	}
	return nil
}

type squadRegistration struct {
	Name   string `json:"display_name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// castingState accepts the qualified managed pair or entirely absent state.
// Legacy/orphan state requires native human review, never implicit migration
// after cast has already written the charter, roster and routing.
func (c *SquadCreator) castingState() (map[string]json.RawMessage, error) {
	bad := func() (map[string]json.RawMessage, error) { return nil, refusal("squad_recovery_required") }
	names := []string{"registry.json", "history.json", "registry-history.commit.json"}
	raw := make([][]byte, len(names))
	objects := make([]map[string]json.RawMessage, len(names))
	present := 0
	for i, name := range names {
		b, err := c.readLocal(".squad/casting/" + name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !kmruntime.JSONKeysUnique(b) || json.Unmarshal(b, &objects[i]) != nil || objects[i] == nil {
			return bad()
		}
		raw[i] = b
		present++
	}
	if present == 0 {
		return nil, nil
	}
	if present != len(names) {
		return bad()
	}
	text := func(o map[string]json.RawMessage, key string) string {
		var s string
		_ = json.Unmarshal(o[key], &s)
		return s
	}
	number := func(o map[string]json.RawMessage, key string) int64 {
		var n int64
		_ = json.Unmarshal(o[key], &n)
		return n
	}
	r, h, m := objects[0], objects[1], objects[2]
	tx, rev := text(m, "transaction_id"), number(m, "registry_revision")
	if number(m, "version") != 1 || tx == "" || rev < 1 || rev > 1<<53-1 || text(r, "schema") != "squad-agent-provenance/v1" || number(r, "schema_version") != 1 || number(r, "revision") != rev || number(h, "registry_revision") != rev || text(r, "transaction_id") != tx || text(h, "transaction_id") != tx {
		return bad()
	}
	if text(m, "registry_sha256") != fmt.Sprintf("%x", sha256.Sum256(raw[0])) || text(m, "history_sha256") != fmt.Sprintf("%x", sha256.Sum256(raw[1])) {
		return bad()
	}
	var snapshots map[string]json.RawMessage
	var usage []json.RawMessage
	if json.Unmarshal(h["assignment_cast_snapshots"], &snapshots) != nil || snapshots == nil || json.Unmarshal(h["universe_usage_history"], &usage) != nil || usage == nil {
		return bad()
	}
	return r, nil
}

func (c *SquadCreator) registry() (map[string]squadRegistration, error) {
	outer, err := c.castingState()
	if err != nil {
		return nil, err
	}
	if outer == nil {
		return map[string]squadRegistration{}, nil
	}
	var agents map[string]map[string]json.RawMessage
	if json.Unmarshal(outer["agents"], &agents) != nil || agents == nil {
		return nil, refusal("unsafe_squad_root")
	}
	out := make(map[string]squadRegistration, len(agents))
	for id, fields := range agents {
		var entry squadRegistration
		_ = json.Unmarshal(fields["display_name"], &entry.Name)
		_ = json.Unmarshal(fields["role"], &entry.Role)
		_ = json.Unmarshal(fields["status"], &entry.Status)
		out[id] = entry
	}
	return out, nil
}
func (c *SquadCreator) registered(m SquadMember) bool {
	agents, err := c.registry()
	if err != nil {
		return false
	}
	a, ok := agents[m.ID]
	team, err := c.readLocal(".squad/team.md")
	return ok && err == nil && a.Name == m.ID && a.Role == m.DisplayRole && a.Status == "active" && bytes.Contains(team, []byte(".squad/agents/"+m.ID+"/charter.md"))
}

func changedSquadPaths(before, after map[string]string) []string {
	paths := map[string]bool{}
	for p, v := range before {
		if after[p] != v {
			paths[p] = true
		}
	}
	for p, v := range after {
		if before[p] != v {
			paths[p] = true
		}
	}
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
