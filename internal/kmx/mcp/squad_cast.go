package mcp

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// SquadProgram is trusted startup configuration, never member/tool input. The
// operator must separately qualify this installed program/runtime for Release
// and platform. These declarations are not binary authentication or a downloader.
type SquadProgram struct{ Path, Release, GOOS, GOARCH string }

// SquadPlan contains an already-approved composition and owned charter changes.
// Approval enforcement/presentation belongs to the host, not this data object.
type SquadPlan struct {
	Lead        SquadMember
	Specialists []SquadMember
	Brief       *string
}
type SquadMemberResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
	File  File   `json:"file"`
}

// SquadResult records authoring observations, never evaluation or dispatch proof.
// ChangedPaths covers retained files in the observed native footprint, not an
// exhaustive event log of transient child writes.
type SquadResult struct {
	Members        []SquadMemberResult `json:"members"`
	ChangedPaths   []string            `json:"changedPaths"`
	EffectsUnknown bool                `json:"effectsUnknown"`
	Setup          string              `json:"setup,omitempty"`
	Brief          *File               `json:"brief,omitempty"`
}

// SquadCreator serializes its own calls. Other native processes/editors must
// respect the operator's quiet project window; the child is not a sandbox.
type SquadCreator struct {
	root        *os.Root
	program     string
	programInfo os.FileInfo
	gate        chan struct{}
	allowSetup  bool
}

func OpenSquad(root string, program SquadProgram) (*SquadCreator, error) {
	return openSquad(root, program, false)
}

func openSquad(root string, program SquadProgram, allowSetup bool) (*SquadCreator, error) {
	if program.Release != "1.1.0" || program.GOOS != runtime.GOOS || program.GOARCH != runtime.GOARCH || !filepath.IsAbs(program.Path) {
		return nil, refusal("unsupported_squad_program")
	}
	executable, err := filepath.EvalSymlinks(program.Path)
	if err != nil {
		return nil, refusal("unsupported_squad_program")
	}
	ext := strings.ToLower(filepath.Ext(executable))
	if ext == ".cmd" || ext == ".bat" || ext == ".ps1" || (runtime.GOOS == "windows" && ext != ".exe") {
		return nil, refusal("unsupported_squad_program")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return nil, refusal("unsupported_squad_program")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || personalSquadRoot(root) {
		return nil, refusal("unsafe_squad_root")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, refusal("unsafe_squad_root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, refusal("unsafe_squad_root")
	}
	c := &SquadCreator{root: r, program: executable, programInfo: info, gate: make(chan struct{}, 1), allowSetup: allowSetup}
	// Native state is validated per operation. An inconsistent member must be
	// inspectable as not set up, not prevent the canonical MCP server starting.
	check := c.checkRoot
	if allowSetup {
		check = func() error { return nil }
	}
	if err := check(); err != nil {
		r.Close()
		return nil, err
	}
	return c, nil
}
func (c *SquadCreator) Close() error { return c.root.Close() }

func personalSquadRoot(root string) bool {
	physical := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	home, _ := os.UserHomeDir()
	if home != "" && filepath.Clean(root) == physical(home) {
		return true
	}
	config, _ := os.UserConfigDir()
	for _, base := range []string{filepath.Join(config, "squad"), os.Getenv("SQUAD_HOME"), os.Getenv("SQUAD_PERSONAL_DIR")} {
		if base == "" || !filepath.IsAbs(base) {
			continue
		}
		rel, err := filepath.Rel(physical(base), root)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			return true
		}
	}
	return false
}

func (c *SquadCreator) Create(ctx context.Context, plan SquadPlan) (result SquadResult, err error) {
	if plan.Brief != nil {
		if err := validateSquadBrief(squadBriefPath, []byte(*plan.Brief)); err != nil {
			return result, err
		}
	}
	members := append([]SquadMember{plan.Lead}, slices.Clone(plan.Specialists)...)
	if len(members) > 8 {
		return result, refusal("invalid_squad_members")
	}
	ids := map[string]bool{}
	total := 0
	for _, m := range members {
		if err := validateSquadMember(m); err != nil {
			return result, err
		}
		if ids[m.ID] {
			return result, refusal("invalid_squad_members")
		}
		ids[m.ID] = true
		total += len(m.ID) + len(m.Role) + len(m.DisplayRole) + len(m.TaskInstructions) + len(m.Model) + len(m.Examples)
		result.Members = append(result.Members, SquadMemberResult{ID: m.ID, State: "not_attempted", File: File{Path: squadPath(m)}})
	}
	if total > 1<<20 {
		return result, refusal("invalid_squad_members")
	}
	// Native routing/TS collision detection is textual. A role matching a later
	// member ID would manufacture a collision after the first cast's effects.
	for _, m := range members {
		for _, other := range members {
			if m.ID != other.ID && (m.Role == other.ID || m.DisplayRole == other.ID) {
				return result, refusal("invalid_squad_members")
			}
		}
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return result, cancelled(ctx)
	}
	if err := cancelled(ctx); err != nil {
		return result, err
	}
	missing := c.allowSetup && c.setupMissing()
	check := c.checkRoot
	if missing {
		check = c.checkSetupRoot
	}
	if err := check(); err != nil {
		return result, err
	}
	if plan.Brief != nil {
		if _, err := c.root.Lstat(squadBriefPath); err == nil {
			return result, refusal("already_exists")
		} else if !os.IsNotExist(err) {
			return result, refusal("io_failed")
		}
	}
	before, err := c.snapshot()
	if err != nil {
		return result, err
	}
	if !missing {
		for _, m := range members {
			if err := c.preflightMember(m, before); err != nil {
				return result, err
			}
		}
	}
	started := false
	defer func() {
		if !started {
			return
		}
		after, observeErr := c.snapshot()
		if observeErr != nil {
			result.EffectsUnknown = true
			if err == nil {
				err = refusal("squad_effects_unknown")
			}
			return
		}
		result.ChangedPaths = changedSquadPaths(before, after)
	}()
	if c.allowSetup {
		result.Setup = "reused"
	}
	if missing {
		if err := cancelled(ctx); err != nil {
			return result, err
		}
		started = true
		result.Setup = "failed"
		succeeded, initErr := c.invoke(ctx, []string{"init", "--no-workflows", "--no-vscode-default"})
		if initErr != nil {
			if succeeded {
				result.Setup = "unknown"
			}
			result.EffectsUnknown = true
			return result, initErr
		}
		if err := c.checkRoot(); err != nil {
			result.EffectsUnknown = true
			return result, err
		}
		if err := c.checkSetupMCP(); err != nil {
			result.EffectsUnknown = true
			return result, err
		}
		result.Setup = "initialized"
		current, err := c.snapshot()
		if err != nil {
			result.EffectsUnknown = true
			return result, err
		}
		for _, m := range members {
			if err := c.preflightMember(m, current); err != nil {
				return result, err
			}
		}
	}
	for i, m := range members {
		if err := cancelled(ctx); err != nil {
			return result, err
		}
		if err := c.checkRoot(); err != nil {
			return result, err
		}
		current, err := c.snapshot()
		if err != nil {
			return result, err
		}
		if err := c.preflightMember(m, current); err != nil {
			return result, err
		}
		info, err := os.Stat(c.program)
		if err != nil || !os.SameFile(c.programInfo, info) || info.Size() != c.programInfo.Size() || info.ModTime() != c.programInfo.ModTime() {
			return result, refusal("unsupported_squad_program")
		}
		started = true
		row := &result.Members[i]
		row.State = "cast_failed"
		succeeded, castErr := c.cast(ctx, m)
		if castErr != nil {
			result.EffectsUnknown = true
			if succeeded {
				row.State = "cast_unknown"
			}
			if succeeded && c.checkRoot() == nil && c.registered(m) {
				row.State = "registered_unenriched"
				if e, ok := castErr.(*Error); ok && e.Code == "squad_output_limit" {
					result.EffectsUnknown = false
				}
			}
			return result, castErr
		}
		row.State = "cast_unknown"
		if err := c.checkRoot(); err != nil {
			result.EffectsUnknown = true
			return result, err
		}
		if !c.registered(m) {
			result.EffectsUnknown = true
			return result, refusal("squad_registration_unknown")
		}
		row.State = "registered_unenriched"
		f := &Files{root: c.root, validate: func(p string, b []byte) error { _, err := agentfile.ParseSquad(p, b, squadProfile(m)); return err }}
		parent, err := f.parent(squadPath(m), false)
		if err != nil {
			return result, squadEnrichmentError(err)
		}
		target, raw, fileInfo, readErr := readTarget(parent, path.Base(squadPath(m)))
		if readErr != nil {
			parent.Close()
			return result, squadEnrichmentError(readErr)
		}
		target.Close()
		parentInfo, statErr := parent.Stat(".")
		parent.Close()
		if statErr != nil {
			return result, refusal("unsafe_squad_root")
		}
		row.File.Revision = revision(raw)
		candidate, err := enrichSquad(m, raw)
		if err != nil {
			return result, squadEnrichmentError(err)
		}
		published, err := f.editObserved(ctx, squadPath(m), row.File.Revision, candidate, &sourceIdentity{file: fileInfo, parent: parentInfo})
		if err != nil {
			return result, squadEnrichmentError(err)
		}
		// Do not turn an acknowledged rename into "unenriched" if cancellation
		// arrives afterwards. Later members still observe cancellation normally.
		row.State = "enriched"
		row.File = published
	}
	if plan.Brief != nil {
		file, err := c.saveBrief(ctx, "none", *plan.Brief)
		if err != nil {
			return result, err
		}
		result.Brief = &file
	}
	return result, nil
}

func squadEnvironment() []string {
	out := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		switch upper {
		case "PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "TMPDIR", "LANG", "LANGUAGE", "TZ":
			out = append(out, entry)
		default:
			if strings.HasPrefix(upper, "LC_") {
				out = append(out, entry)
			}
		}
	}
	return append(out, "SQUAD_NO_PERSONAL=1")
}
