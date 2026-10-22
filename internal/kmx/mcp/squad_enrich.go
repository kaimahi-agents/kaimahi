package mcp

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// SquadMember contains approved authored content, not a safe log projection.
// Role is the CLI catalog key/literal; DisplayRole is the approved native title.
// Examples is one fenced YAML document, using the existing examples mapping.
type SquadMember struct {
	ID, Role, DisplayRole, TaskInstructions, Model, Examples string
}

func validateSquadMember(m SquadMember) error {
	bad := func() error { return refusal("invalid_squad_member") }
	if !safeID(m.ID) || strings.Contains(m.ID, "--") || strings.HasSuffix(m.ID, "-") {
		return bad()
	}
	switch m.ID {
	case "squad", "scribe", "ralph", "rai", "fact-checker", "factchecker":
		return bad()
	}
	total := len(m.ID) + len(m.Role) + len(m.DisplayRole) + len(m.TaskInstructions) + len(m.Model) + len(m.Examples)
	if total > maxFileBytes || strings.TrimSpace(m.TaskInstructions) == "" {
		return bad()
	}
	for _, role := range []string{m.Role, m.DisplayRole} {
		if role == "" || len(role) > 256 || !utf8.ValidString(role) || role != strings.TrimSpace(role) || strings.HasPrefix(role, "-") {
			return bad()
		}
		// Native cast interpolates roles into Markdown and single-quoted TS.
		for _, r := range role {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -/+&()#.,:", r) {
				return bad()
			}
		}
	}
	// This temporary display-only input reuses common source/model/Examples
	// validation. It is never the native output or an execution authorization.
	probe := "# " + m.Role + "\n\n" + m.DisplayRole + "\n\n" + m.TaskInstructions +
		"\n\n## Model\n\n- **Preferred:** " + m.Model + "\n\n## Examples\n\n" + m.Examples + "\n"
	a, err := agentfile.Inspect(m.ID+".agent.md", []byte(probe))
	if err != nil || a.Model != m.Model {
		return bad()
	}
	for field := range a.Locations {
		switch field {
		case "Title", "Model", "Model.Preferred", "Examples":
		default:
			return bad()
		}
	}
	return nil
}

func squadProfile(m SquadMember) agentfile.SquadProfile {
	return agentfile.SquadProfile{Release: "1.1.0", MemberID: m.ID, Role: m.DisplayRole}
}
func squadPath(m SquadMember) string { return ".squad/agents/" + m.ID + "/charter.md" }

// enrichSquad changes only the existing Preferred value and appends owned fields.
// The caller must establish cast provenance and revision/inode-bound publication.
func enrichSquad(m SquadMember, starter []byte) ([]byte, error) {
	if err := validateSquadMember(m); err != nil {
		return nil, err
	}
	doc, err := agentfile.ParseSquad(squadPath(m), starter, squadProfile(m))
	if err != nil || doc.Enriched {
		return nil, refusal("invalid_squad_starter")
	}
	raw := doc.Source()
	out := make([]byte, 0, len(raw)+len(m.TaskInstructions)+len(m.Examples)+256)
	if value := doc.Preferred; value != nil {
		out = append(out, raw[:value.Start]...)
		out = append(out, m.Model...)
		out = append(out, raw[value.End:]...)
	} else {
		out = append(out, raw...)
		out = append(out, "\n\n## Model\n\n- **Preferred:** "...)
		out = append(out, m.Model...)
	}
	// Even native EOF whitespace is retained. New separators belong to kmx.
	out = append(out, "\n\n## Task Instructions\n\n"...)
	out = append(out, m.TaskInstructions...)
	out = append(out, "\n\n## Examples\n\n"...)
	out = append(out, m.Examples...)
	out = append(out, '\n')
	parsed, err := agentfile.ParseSquad(squadPath(m), out, squadProfile(m))
	if err != nil || !parsed.Enriched {
		return nil, refusal("invalid_squad_enrichment")
	}
	return out, nil
}
