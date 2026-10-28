package mcp

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// Member reconstructs a supported binding from native state, not a kmx origin
// claim. A valid profile never recovers initial-enrichment provenance or a pass.
func (c *SquadCreator) Member(ctx context.Context, id string) (*agentfile.SquadCharter, error) {
	d, _, err := c.readMember(ctx, id)
	return d, err
}

type squadMemberInventory struct {
	registry map[string]squadRegistration
	roster   []byte
}

// One request owns this checked inventory; it is never cached across reads.
func (c *SquadCreator) memberInventory(ctx context.Context) (squadMemberInventory, error) {
	var inventory squadMemberInventory
	if err := cancelled(ctx); err != nil {
		return inventory, err
	}
	if err := c.checkRoot(); err != nil {
		return inventory, err
	}
	agents, err := c.registry()
	if err != nil {
		return inventory, err
	}
	inventory.registry = agents
	// Committed hashes alone are insufficient while native recovery is pending.
	seen, err := c.snapshot()
	if err != nil || squadRecoveryRequired(seen) {
		// Preserve known IDs without qualifying any member.
		return inventory, nil
	}
	inventory.roster, _ = c.readLocal(".squad/team.md")
	return inventory, nil
}

func (c *SquadCreator) readMember(ctx context.Context, id string) (*agentfile.SquadCharter, *sourceIdentity, error) {
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	if !safeID(id) {
		return nil, nil, refusal("squad_not_setup")
	}
	inventory, err := c.memberInventory(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, cancelled(ctx)
		}
		return nil, nil, refusal("squad_not_setup")
	}
	return c.readMemberFromInventory(ctx, id, inventory)
}

func (c *SquadCreator) readMemberFromInventory(ctx context.Context, id string, inventory squadMemberInventory) (*agentfile.SquadCharter, *sourceIdentity, error) {
	bad := func() (*agentfile.SquadCharter, *sourceIdentity, error) { return nil, nil, refusal("squad_not_setup") }
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	if !safeID(id) || inventory.roster == nil {
		return bad()
	}
	entry, ok := inventory.registry[id]
	if !ok || entry.Name != id || entry.Status != "active" || entry.Role == "" {
		return bad()
	}
	m := SquadMember{ID: id, DisplayRole: entry.Role}
	if !memberRosterMatches(inventory.roster, m) {
		return bad()
	}
	f := &Files{root: c.root}
	parent, err := f.parent(squadPath(m), false)
	if err != nil {
		return bad()
	}
	defer parent.Close()
	file, raw, info, err := readTarget(parent, path.Base(squadPath(m)))
	if err != nil {
		return bad()
	}
	file.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return bad()
	}
	observed := &sourceIdentity{file: info, parent: parentInfo}
	if !f.observedParent(squadPath(m), parent, observed) {
		return bad()
	}
	d, err := agentfile.ParseSquad(squadPath(m), raw, squadProfile(m))
	if err != nil || !d.Enriched {
		return bad()
	}
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	return d, observed, nil
}
func memberRosterMatches(raw []byte, m SquadMember) bool {
	count := 0
	inMembers, foundMembers := false, false
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		// Qualified native rosters contain no fenced blocks; never accept
		// headings/rows inside unsupported Markdown as binding metadata.
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			return false
		}
		if strings.HasPrefix(line, "## ") {
			inMembers = line == "## Members"
			if inMembers {
				if foundMembers {
					return false
				}
				foundMembers = true
			}
			continue
		}
		if !inMembers {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 6 || strings.TrimSpace(fields[0]) != "" || strings.TrimSpace(fields[5]) != "" {
			continue
		}
		if strings.TrimSpace(fields[1]) != m.ID {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(fields[4]))
		if strings.TrimSpace(fields[2]) != m.DisplayRole || strings.TrimSpace(fields[3]) != "`"+squadPath(m)+"`" || (status != "active" && status != "✅ active") {
			return false
		}
		count++
	}
	return count == 1
}

// EditMember changes only owned fields. Reopening a valid enriched member is
// not adoption of a bare starter; native identity/role/ownership remain native.
func (c *SquadCreator) EditMember(ctx context.Context, id, expected string, m SquadMember) (File, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return File{}, cancelled(ctx)
	}
	d, observed, err := c.readMember(ctx, id)
	if err != nil {
		return File{}, err
	}
	if d.Revision != expected {
		return File{}, refusal("stale_revision")
	}
	m.ID = id
	m.Role = d.Profile.Role
	m.DisplayRole = d.Profile.Role
	if err := validateSquadMember(m); err != nil {
		return File{}, err
	}
	type replacement struct {
		span agentfile.ByteSpan
		body string
	}
	changes := []replacement{{*d.Preferred, m.Model}}
	for _, section := range d.Sections {
		switch section.Name {
		case "Task Instructions":
			changes = append(changes, replacement{section.Body, "\n" + m.TaskInstructions + "\n\n"})
		case "Examples":
			changes = append(changes, replacement{section.Body, "\n" + m.Examples + "\n"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].span.Start > changes[j].span.Start })
	raw := d.Source()
	for _, change := range changes {
		b := append([]byte{}, raw[:change.span.Start]...)
		b = append(b, change.body...)
		raw = append(b, raw[change.span.End:]...)
	}
	if _, err := agentfile.ParseSquad(squadPath(m), raw, d.Profile); err != nil {
		return File{}, refusal("invalid_squad_member")
	}
	f := &Files{root: c.root, validate: func(p string, b []byte) error { _, err := agentfile.ParseSquad(p, b, d.Profile); return err }}
	file, err := f.editObserved(ctx, squadPath(m), expected, raw, observed)
	if err != nil {
		return File{}, err
	}
	return file, nil
}
