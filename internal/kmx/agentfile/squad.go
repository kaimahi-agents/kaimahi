package agentfile

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// SquadProfile selects qualified native parsing and an approved cast mapping.
// Role is the native display role, not a role-catalog lookup ID. This input is
// not evidence of file origin, registration, or filesystem confinement.
type SquadProfile struct{ Release, MemberID, Role string }
type SquadIdentity struct{ Name, Role string }

// ByteSpan is a half-open range in the original, non-normalized source.
type ByteSpan struct{ Start, End int }
type SquadSection struct {
	Name       string
	Span, Body ByteSpan
}

// SquadCharter is not an Agent: a starter cannot enter existing preview routes.
// Parsed fields contain authored content and are not safe log projections.
// Enriched means additions validate, not that evaluation or registration passed.
type SquadCharter struct {
	Profile                                                SquadProfile
	Identity                                               SquadIdentity
	Title, Instructions, TaskInstructions, Model, Revision string
	Examples                                               []Example
	Enriched                                               bool
	Locations                                              map[string]Location
	Sections                                               []SquadSection
	Preferred                                              *ByteSpan
	source                                                 []byte
}

// Source returns a copy, preserving original line endings and the final byte.
func (d *SquadCharter) Source() []byte { return bytes.Clone(d.source) }

const squadAttribution = "<!-- Adapted from agency-agents by AgentLand Contributors (MIT License) — https://github.com/msitarzewski/agency-agents -->"

var squadField = regexp.MustCompile(`^- \*\*([A-Za-z]+):\*\*[ \t]*(.*)$`)

var squadNativeSections = []string{"Identity", "What I Own", "How I Work", "Boundaries", "Collaboration", "Voice"}

type squadLine struct {
	text       string
	start, end int
}

// ParseSquad explicitly accepts the qualified Squad 1.1.0 catalog/fallback
// shapes and their bounded enrichment. It neither reads files nor runs Squad.
// Placement is a lexical identity check, not a rooted-file security check.
func ParseSquad(filename string, data []byte, profile SquadProfile) (*SquadCharter, error) {
	fail := func(field, problem string) (*SquadCharter, error) {
		return nil, refuse(filename, 1, 1, field, problem)
	}
	if profile.Release != "1.1.0" {
		return fail("Profile", "select qualified Squad release 1.1.0; qualify upgrades before parsing")
	}
	reserved := []string{"squad", "scribe", "ralph", "rai", "fact-checker", "factchecker"}
	if slices.Contains(reserved, strings.ToLower(profile.MemberID)) || !agentID.MatchString(profile.MemberID) || strings.Contains(profile.MemberID, "--") || strings.HasSuffix(profile.MemberID, "-") {
		return fail("Profile.MemberID", "use a nonreserved lowercase cast ID, starting with a letter, with single internal hyphens and at most 63 characters")
	}
	if profile.Role == "" || len(profile.Role) > 256 || strings.TrimSpace(profile.Role) != profile.Role || strings.IndexFunc(profile.Role, unicode.IsControl) >= 0 {
		return fail("Profile.Role", "supply the approved nonblank single-line native display role, at most 256 bytes")
	}
	slash := path.Clean(strings.ReplaceAll(filename, "\\", "/"))
	parts := strings.Split(slash, "/")
	if len(parts) < 4 || !strings.EqualFold(parts[len(parts)-4], ".squad") || !strings.EqualFold(parts[len(parts)-3], "agents") || parts[len(parts)-2] != profile.MemberID || !strings.EqualFold(parts[len(parts)-1], "charter.md") {
		return fail("File", "use .squad/agents/<approved-id>/charter.md; do not infer a cast ID from a filename")
	}
	if _, err := checkedSourceText(filename, data); err != nil {
		return nil, err
	}
	lines := []squadLine{}
	offset := 0
	for _, raw := range strings.Split(string(data), "\n") {
		lines = append(lines, squadLine{strings.TrimSuffix(raw, "\r"), offset, min(offset+len(raw)+1, len(data))})
		offset += len(raw) + 1
	}
	d := &SquadCharter{Profile: profile, Revision: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Locations: map[string]Location{}, source: bytes.Clone(data)}
	sections := map[string]int{}
	current, nextNative := -1, 0
	var open fence
	quote, attribution := false, false
	for i, line := range lines {
		if open.count > 0 {
			if closeFence(line.text, open) {
				open = fence{}
			}
			continue
		}
		if strings.TrimSpace(line.text) == "" {
			continue
		}
		if d.Title == "" {
			if line.text != "# "+profile.MemberID+" — "+profile.Role {
				return nil, refuse(filename, i+1, 1, "Title", "match the approved cast name and native display role in one # title")
			}
			d.Title = line.text[2:]
			d.Locations["Title"] = Location{filename, i + 1, 1}
			continue
		}
		if strings.HasPrefix(line.text, "# ") {
			return nil, refuse(filename, i+1, 1, "Title", "keep one native # title")
		}
		if line.text == squadAttribution && current == -1 && !attribution {
			attribution = true
			continue
		}
		if strings.Contains(line.text, "<!--") {
			return nil, refuse(filename, i+1, 1, "Sections", "preserve only the qualified native attribution before Identity; other HTML comments are unsupported")
		}
		trimmed := strings.TrimLeft(line.text, " \t")
		if strings.HasPrefix(trimmed, "## ") || trimmed == "##" || strings.HasPrefix(trimmed, "##\t") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			if current >= 0 && d.Sections[current].Name == "Examples" {
				return nil, refuse(filename, i+1, 1, "Examples", "keep Examples last")
			}
			known := slices.Contains(squadNativeSections, name) || name == "Model" || name == "Task Instructions" || name == "Examples"
			if !known || line.text != "## "+name {
				return nil, refuse(filename, i+1, 1, "Sections", "use the qualified unindented native headings; qualify unsupported template drift before enrichment")
			}
			if _, exists := sections[name]; exists {
				return nil, refuse(filename, i+1, 1, name, "keep one structural section")
			}
			switch name {
			case "Model":
				_, task := sections["Task Instructions"]
				if task || (nextNative != 4 && nextNative != 6) {
					return nil, refuse(filename, i+1, 1, "Sections", "keep native Model before Collaboration, or append missing Model immediately before Task Instructions")
				}
			case "Task Instructions":
				if nextNative != len(squadNativeSections) {
					return nil, refuse(filename, i+1, 1, "Sections", "preserve every native section before Task Instructions")
				}
			case "Examples":
				if _, task := sections["Task Instructions"]; !task {
					return nil, refuse(filename, i+1, 1, "Task Instructions", "add Task Instructions before Examples")
				}
			default:
				if nextNative >= len(squadNativeSections) || name != squadNativeSections[nextNative] {
					return nil, refuse(filename, i+1, 1, "Sections", "preserve the qualified native section order; review unsupported drift")
				}
				nextNative++
			}
			if current >= 0 {
				d.Sections[current].Span.End = line.start
				d.Sections[current].Body.End = line.start
			}
			current = len(d.Sections)
			sections[name] = current
			d.Sections = append(d.Sections, SquadSection{name, ByteSpan{line.start, len(data)}, ByteSpan{line.end, len(data)}})
			d.Locations[name] = Location{filename, i + 1, 1}
		} else if current == -1 {
			if !quote && strings.HasPrefix(line.text, "> ") && strings.TrimSpace(line.text[2:]) != "" {
				quote = true
				continue
			}
			return nil, refuse(filename, i+1, 1, "Sections", "keep only the native quote and attribution before Identity")
		}
		if f, _, ok := openFence(line.text); ok {
			open = f
		}
	}
	if d.Title == "" {
		return fail("Title", "start with the approved native cast title")
	}
	if open.count > 0 {
		field := "Sections"
		if current >= 0 && d.Sections[current].Name == "Examples" {
			field = "Examples"
		}
		return nil, refuse(filename, len(lines), 1, field, "close the Markdown fence with matching markers")
	}
	if nextNative != len(squadNativeSections) {
		return fail("Sections", "keep all qualified native sections; review drift instead of reconstructing the charter")
	}
	for _, name := range squadNativeSections {
		s := d.Sections[sections[name]]
		if strings.TrimSpace(string(data[s.Body.Start:s.Body.End])) == "" {
			return fail(name, "preserve nonblank native content")
		}
	}
	identity, _, err := d.readMetadata(filename, lines, "Identity", []string{"Name", "Role", "Expertise", "Style"})
	if err != nil {
		return nil, err
	}
	if identity["Role"] != profile.Role {
		return fail("Identity.Role", "match the approved native display role")
	}
	if name, present := identity["Name"]; present && name != profile.MemberID {
		return fail("Identity.Name", "match the approved cast name without changing native identity fields")
	}
	d.Identity = SquadIdentity{identity["Name"], identity["Role"]}
	if _, present := sections["Model"]; present {
		model, spans, err := d.readMetadata(filename, lines, "Model", []string{"Preferred", "Rationale", "Fallback"})
		if err != nil {
			return nil, err
		}
		d.Model = model["Preferred"]
		if d.Model == "" || (d.Model != "auto" && !concreteSquadModel(d.Model)) {
			return fail("Model.Preferred", "keep one native auto preference or a concrete exact model ID")
		}
		value := spans["Preferred"]
		d.Preferred = &value
	}
	taskIndex, hasTask := sections["Task Instructions"]
	examplesIndex, hasExamples := sections["Examples"]
	if hasTask && !hasExamples {
		return fail("Examples", "add valid Examples last; a partial enrichment is unsupported")
	}
	d.Instructions = strings.ReplaceAll(string(data), "\r\n", "\n")
	if hasTask {
		if !concreteSquadModel(d.Model) {
			return fail("Model.Preferred", "set one concrete exact model ID before kmx execution; native Fallback grants no fallback authority")
		}
		task := d.Sections[taskIndex].Body
		d.TaskInstructions = strings.ReplaceAll(string(data[task.Start:task.End]), "\r\n", "\n")
		if strings.TrimSpace(d.TaskInstructions) == "" {
			return fail("Task Instructions", "add bounded task instructions without replacing native rules")
		}
		examples := d.Sections[examplesIndex]
		d.Examples, err = parseExamples(filename, strings.Split(strings.ReplaceAll(string(data[examples.Body.Start:examples.Body.End]), "\r\n", "\n"), "\n"), d.Locations["Examples"].Line+1)
		if err != nil {
			return nil, err
		}
		d.Instructions = strings.ReplaceAll(string(data[:examples.Span.Start]), "\r\n", "\n")
		d.Enriched = true
	}
	return d, nil
}

func concreteSquadModel(model string) bool {
	return modelID.MatchString(model) && !strings.EqualFold(model, "auto") && !strings.Contains(model, "://")
}

func (d *SquadCharter) readMetadata(filename string, lines []squadLine, name string, allowed []string) (map[string]string, map[string]ByteSpan, error) {
	var body ByteSpan
	for _, s := range d.Sections {
		if s.Name == name {
			body = s.Body
			break
		}
	}
	values, spans := map[string]string{}, map[string]ByteSpan{}
	for i, line := range lines {
		if line.start < body.Start || line.start >= body.End || strings.TrimSpace(line.text) == "" {
			continue
		}
		match := squadField.FindStringSubmatchIndex(line.text)
		if match == nil {
			return nil, nil, refuse(filename, i+1, 1, name, "preserve only the qualified native bullet metadata fields")
		}
		key, raw := line.text[match[2]:match[3]], line.text[match[4]:match[5]]
		if !slices.Contains(allowed, key) {
			return nil, nil, refuse(filename, i+1, 1, name, "preserve only the qualified native bullet metadata fields")
		}
		if _, exists := values[key]; exists {
			return nil, nil, refuse(filename, i+1, 1, name, "remove duplicate metadata fields")
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, nil, refuse(filename, i+1, 1, name+"."+key, "keep present native metadata nonblank")
		}
		start := line.start + match[4] + len(raw) - len(strings.TrimLeftFunc(raw, unicode.IsSpace))
		values[key], spans[key] = value, ByteSpan{start, start + len(value)}
		d.Locations[name+"."+key] = Location{filename, i + 1, start - line.start + 1}
	}
	return values, spans, nil
}
